// Package adapter implements the OpenAI compatible HTTP surface
// folded into the clyde daemon. A single launchd entry boots the
// gRPC daemon plus this adapter so clients pointing at a local URL
// can drive the Claude Max subscription through claude -p. The
// package is intentionally small so the monolith stays cohesive.
package adapter

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"goodkind.io/clyde/internal/config"
)

// Backend names the kind of worker that fulfils a request.
const (
	BackendClaude = "claude"
	BackendShunt  = "shunt"
	// BackendAnthropic routes the request directly at
	// https://REDACTED-UPSTREAM/v1/messages. Auth is via the
	// user's Claude.ai OAuth token from the keychain (the
	// internal/adapter/oauth package handles the credential side;
	// internal/adapter/anthropic handles the HTTP side). Selected
	// when adapter config sets direct_oauth=true; the registry
	// rewrites every BackendClaude model to BackendAnthropic at
	// construction so the HTTP layer only has to switch on this
	// single value.
	BackendAnthropic = "anthropic"
	// BackendFallback routes the request through the local
	// `claude` CLI in `-p --output-format stream-json` mode. This
	// is the third backend, configured via [adapter.fallback];
	// the dispatcher selects it either explicitly (alias bound to
	// backend = "fallback") or as an on-oauth-failure escalation.
	BackendFallback = "fallback"
)

// FallbackTrigger values control when the fallback dispatcher fires.
const (
	FallbackTriggerExplicit       = "explicit"
	FallbackTriggerOnOAuthFailure = "on_oauth_failure"
	FallbackTriggerBoth           = "both"
)

// FallbackEscalation values control which error surfaces when both
// the OAuth attempt and the fallback attempt fail.
const (
	FallbackEscalationFallbackError = "fallback_error"
	FallbackEscalationOAuthError    = "oauth_error"
)

// Effort tiers as defined in Claude Code's src/utils/effort.ts
// (EFFORT_LEVELS = ['low', 'medium', 'high', 'max']). Sent inside
// output_config on /v1/messages, gated by the effort-2025-11-24
// beta header. Whether a given family accepts a given tier is
// declared in the user's toml under [adapter.families.<name>.efforts].
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortMax    = "max"
)

// Thinking modes mirror the three ThinkingConfig shapes from Claude
// Code's src/utils/thinking.ts plus the implicit "default" baseline.
// The adapter translates each value into the right wire payload on
// the OAuth direct path. `default` means "send no thinking block"
// so the server applies its per-model default.
const (
	ThinkingDefault  = "default"
	ThinkingAdaptive = "adaptive"
	ThinkingEnabled  = "enabled"
	ThinkingDisabled = "disabled"
)

// ResolvedModel is the runtime view of one alias. The registry maps
// every incoming model string to exactly one ResolvedModel.
type ResolvedModel struct {
	// Alias is the public name the client sent.
	Alias string
	// Backend is one of BackendClaude / BackendShunt / BackendAnthropic / BackendFallback.
	Backend string
	// ClaudeModel names the real Claude snapshot. May carry a
	// context-window wire suffix (e.g. "[1m]");
	// oauth_handler.stripContextSuffix removes it before the wire
	// call.
	ClaudeModel string
	// Context is the advertised context window in tokens. Purely
	// informational; the upstream enforces the real limit.
	Context int
	// Efforts enumerates the allowed `effort` values for this
	// family. Empty for families the toml leaves without an efforts
	// list; Resolve will then 400 any caller-supplied effort.
	Efforts []string
	// Effort is the bound effort value when the alias encodes one
	// (e.g. clyde-opus-4-7-high-1m -> "high"). Empty when the
	// caller picks per-request via the OpenAI body.
	Effort string
	// ThinkingModes enumerates the allowed `thinking` values.
	ThinkingModes []string
	// Thinking is the bound thinking mode when the alias encodes
	// one. Empty resolves to ThinkingDefault at request time.
	Thinking string
	// MaxOutputTokens caps the family's output tokens. Used to
	// derive the budget_tokens for ThinkingEnabled (budget = max-1
	// per Claude Code's invariant).
	MaxOutputTokens int
	// SupportsTools mirrors the family toml after NewRegistry
	// validation (nil disallowed at load time).
	SupportsTools bool
	// SupportsVision mirrors the family toml after NewRegistry
	// validation (nil disallowed at load time).
	SupportsVision bool
	// Shunt names an entry inside AdapterConfig.Shunts. Only set
	// when Backend is BackendShunt.
	Shunt string
	// CLIAlias is the short model name passed to `claude -p
	// --model` when the fallback dispatcher fires. Populated from
	// cfg.Fallback.CLIAliases[familySlug] during registry
	// construction; empty when the fallback is disabled or the
	// family is not whitelisted.
	CLIAlias string
	// FamilySlug is the cfg.Families key this alias was generated
	// from. Empty for user-supplied [adapter.models.<name>] entries
	// (which carry their own backend/model directly).
	FamilySlug string
}

// Registry owns the resolved model table used by the HTTP layer.
// Construction layers user-supplied per-alias overrides on top of
// the family-expanded aliases. There are no compiled-in defaults:
// NewRegistry rejects an AdapterConfig that omits families,
// impersonation, or default_model.
type Registry struct {
	models       map[string]ResolvedModel
	shunts       map[string]config.AdapterShunt
	def          string
	fallbackShnt string
}

// NewRegistry builds the registry from a loaded AdapterConfig. It
// returns an error when the config is incomplete: no families,
// no default model, or any field of Impersonation empty. Callers
// (currently only the daemon adapter wiring) should refuse to start
// the listener and surface the error so the user sees what is
// missing.
func NewRegistry(cfg config.AdapterConfig) (*Registry, error) {
	if cfg.DefaultModel == "" {
		return nil, fmt.Errorf("adapter: default_model must be set in [adapter]")
	}
	if cfg.Impersonation.BetaHeader == "" {
		return nil, fmt.Errorf("adapter: [adapter.impersonation].beta_header must be set")
	}
	if cfg.Impersonation.UserAgent == "" {
		return nil, fmt.Errorf("adapter: [adapter.impersonation].user_agent must be set")
	}
	if cfg.Impersonation.SystemPromptPrefix == "" {
		return nil, fmt.Errorf("adapter: [adapter.impersonation].system_prompt_prefix must be set")
	}
	toolsCapable := 0
	visionCapable := 0
	for slug, family := range cfg.Families {
		if family.SupportsTools == nil {
			return nil, fmt.Errorf(
				"adapter: family %q missing supports_tools (must be explicit true/false)",
				slug,
			)
		}
		if family.SupportsVision == nil {
			return nil, fmt.Errorf("adapter: family %q missing supports_vision", slug)
		}
		if *family.SupportsTools {
			toolsCapable++
		}
		if *family.SupportsVision {
			visionCapable++
		}
	}
	if err := validateAdapterLogprobs(cfg.Logprobs); err != nil {
		return nil, err
	}
	if len(cfg.Families) == 0 {
		return nil, fmt.Errorf("adapter: no model families declared in [adapter.families]")
	}

	if err := validateFallbackConfig(cfg); err != nil {
		return nil, err
	}

	cliAliases := map[string]string{}
	allowedFamilies := map[string]struct{}{}
	if cfg.Fallback.Enabled {
		for slug, alias := range cfg.Fallback.CLIAliases {
			cliAliases[slug] = alias
		}
		for _, slug := range cfg.Fallback.AllowedFamilies {
			allowedFamilies[slug] = struct{}{}
		}
	}

	models := map[string]ResolvedModel{}
	for slug, family := range cfg.Families {
		if family.Model == "" {
			return nil, fmt.Errorf("adapter: family %q missing model id", slug)
		}
		if len(family.Contexts) == 0 {
			return nil, fmt.Errorf("adapter: family %q missing contexts", slug)
		}
		cliAlias := ""
		if cfg.Fallback.Enabled {
			if _, ok := allowedFamilies[slug]; ok {
				cliAlias = cliAliases[slug]
			}
		}
		generateFamilyAliases(models, slug, family, cliAlias)
	}

	r := &Registry{
		models:       models,
		shunts:       map[string]config.AdapterShunt{},
		def:          cfg.DefaultModel,
		fallbackShnt: strings.ToLower(cfg.FallbackShunt),
	}

	for name, m := range cfg.Models {
		r.models[strings.ToLower(name)] = resolveFromConfig(name, m)
	}
	for name, s := range cfg.Shunts {
		r.shunts[strings.ToLower(name)] = s
	}

	rewritten := 0
	for alias, rm := range r.models {
		rm.Alias = alias
		if cfg.DirectOAuth && rm.Backend == BackendClaude {
			rm.Backend = BackendAnthropic
			rewritten++
		}
		r.models[alias] = rm
	}
	if cfg.DirectOAuth {
		slog.Info("adapter.registry.oauth_rewrite",
			"component", "adapter",
			"models_rewritten", rewritten,
			"models_total", len(r.models),
		)
	}

	if _, ok := r.models[strings.ToLower(r.def)]; !ok {
		// The default model must resolve after expansion. Permit a
		// shunt fallback to absorb it; otherwise hard fail because a
		// daemon that cannot serve its declared default is broken.
		if r.fallbackShnt == "" {
			return nil, fmt.Errorf(
				"adapter: default_model %q not found among %d expanded aliases",
				r.def, len(r.models),
			)
		}
	}

	slog.Info("adapter.registry.capabilities_loaded",
		"component", "adapter",
		"families", len(cfg.Families),
		"tools_capable", toolsCapable,
		"vision_capable", visionCapable,
	)

	return r, nil
}

// validateAdapterLogprobs enforces the logprobs stanza when the user
// set at least one backend key. Empty strings mean the stanza is
// absent or unused; otherwise both backends must be reject or drop.
func validateAdapterLogprobs(lp config.AdapterLogprobs) error {
	if lp.Anthropic == "" && lp.Fallback == "" {
		return nil
	}
	allowed := map[string]bool{"reject": true, "drop": true}
	if lp.Anthropic == "" {
		return fmt.Errorf(
			`adapter: [adapter.logprobs].anthropic must be set ("reject" or "drop")`,
		)
	}
	if !allowed[lp.Anthropic] {
		return fmt.Errorf("adapter: [adapter.logprobs].anthropic %q invalid", lp.Anthropic)
	}
	if lp.Fallback == "" {
		return fmt.Errorf(
			`adapter: [adapter.logprobs].fallback must be set ("reject" or "drop")`,
		)
	}
	if !allowed[lp.Fallback] {
		return fmt.Errorf("adapter: [adapter.logprobs].fallback %q invalid", lp.Fallback)
	}
	return nil
}

// generateFamilyAliases produces the full cross product of effort ×
// thinking × context aliases for one family declaration. Schema:
//
//	clyde-<family>[-<effort>][-<ctx>][-thinking-<mode>]
//
// `thinking-default` and the empty effort sentinel are omitted from
// the alias name so users get the shortest readable name when those
// dimensions don't apply.
func generateFamilyAliases(out map[string]ResolvedModel, slug string, f config.AdapterFamily, cliAlias string) {
	efforts := f.Efforts
	if len(efforts) == 0 {
		efforts = []string{""} // sentinel: do not emit effort segment, do not bind
	}
	thinking := f.ThinkingModes
	if len(thinking) == 0 {
		thinking = []string{""}
	}
	for _, ctx := range f.Contexts {
		for _, eff := range efforts {
			for _, th := range thinking {
				alias := buildAlias(slug, eff, ctx.AliasSuffix, th)
				out[alias] = ResolvedModel{
					Backend:         BackendClaude,
					ClaudeModel:     f.Model + ctx.WireSuffix,
					Context:         ctx.Tokens,
					Efforts:         f.Efforts,
					Effort:          eff,
					ThinkingModes:   f.ThinkingModes,
					Thinking:        th,
					MaxOutputTokens: f.MaxOutputTokens,
					SupportsTools:   *f.SupportsTools,
					SupportsVision:  *f.SupportsVision,
					CLIAlias:        cliAlias,
					FamilySlug:      slug,
				}
			}
		}
	}
}

// validateFallbackConfig enforces the no-defaults / fail-loud contract
// for [adapter.fallback]. Disabled is a no-op; enabled requires every
// field below to be set, every whitelisted family slug to exist, and
// every CLIAlias key to map to a real family. The shunt forwarding
// leg, when enabled, must point at a real shunt.
func validateFallbackConfig(cfg config.AdapterConfig) error {
	fb := cfg.Fallback
	if !fb.Enabled {
		return nil
	}
	switch fb.Trigger {
	case FallbackTriggerExplicit, FallbackTriggerOnOAuthFailure, FallbackTriggerBoth:
	case "":
		return fmt.Errorf("adapter: [adapter.fallback].trigger must be set when enabled")
	default:
		return fmt.Errorf(
			"adapter: [adapter.fallback].trigger %q invalid (allowed: %s, %s, %s)",
			fb.Trigger,
			FallbackTriggerExplicit, FallbackTriggerOnOAuthFailure, FallbackTriggerBoth,
		)
	}
	if fb.Timeout == "" {
		return fmt.Errorf("adapter: [adapter.fallback].timeout must be set when enabled")
	}
	d, err := parseFallbackDuration(fb.Timeout)
	if err != nil {
		return fmt.Errorf("adapter: [adapter.fallback].timeout %q: %w", fb.Timeout, err)
	}
	if d <= 0 {
		return fmt.Errorf("adapter: [adapter.fallback].timeout must be > 0")
	}
	if fb.MaxConcurrent <= 0 {
		return fmt.Errorf("adapter: [adapter.fallback].max_concurrent must be > 0 when enabled")
	}
	if fb.ScratchSubdir == "" {
		return fmt.Errorf("adapter: [adapter.fallback].scratch_subdir must be set when enabled")
	}
	switch fb.FailureEscalation {
	case FallbackEscalationFallbackError, FallbackEscalationOAuthError:
	case "":
		return fmt.Errorf("adapter: [adapter.fallback].failure_escalation must be set when enabled")
	default:
		return fmt.Errorf(
			"adapter: [adapter.fallback].failure_escalation %q invalid (allowed: %s, %s)",
			fb.FailureEscalation,
			FallbackEscalationFallbackError, FallbackEscalationOAuthError,
		)
	}
	if len(fb.AllowedFamilies) == 0 {
		return fmt.Errorf("adapter: [adapter.fallback].allowed_families must be non-empty when enabled")
	}
	for _, slug := range fb.AllowedFamilies {
		if _, ok := cfg.Families[slug]; !ok {
			return fmt.Errorf(
				"adapter: [adapter.fallback].allowed_families references unknown family %q",
				slug,
			)
		}
	}
	if len(fb.CLIAliases) == 0 {
		return fmt.Errorf("adapter: [adapter.fallback.cli_aliases] must declare at least one mapping when enabled")
	}
	for slug, cliName := range fb.CLIAliases {
		if _, ok := cfg.Families[slug]; !ok {
			return fmt.Errorf(
				"adapter: [adapter.fallback.cli_aliases] references unknown family %q",
				slug,
			)
		}
		if cliName == "" {
			return fmt.Errorf(
				"adapter: [adapter.fallback.cli_aliases].%s must be a non-empty CLI model name",
				slug,
			)
		}
	}
	for _, slug := range fb.AllowedFamilies {
		if _, ok := fb.CLIAliases[slug]; !ok {
			return fmt.Errorf(
				"adapter: [adapter.fallback.cli_aliases] missing entry for whitelisted family %q",
				slug,
			)
		}
	}
	if fb.ForwardToShunt.Enabled {
		if fb.ForwardToShunt.Shunt == "" {
			return fmt.Errorf("adapter: [adapter.fallback.forward_to_shunt].shunt must be set when enabled")
		}
		if _, ok := cfg.Shunts[fb.ForwardToShunt.Shunt]; !ok {
			return fmt.Errorf(
				"adapter: [adapter.fallback.forward_to_shunt].shunt %q not declared in [adapter.shunts]",
				fb.ForwardToShunt.Shunt,
			)
		}
	}
	return nil
}

// parseFallbackDuration is a thin wrapper that exists so the Registry
// can validate the duration string without importing time directly
// in NewRegistry's signature. Kept tiny on purpose.
func parseFallbackDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}

// buildAlias assembles one alias from its components.
func buildAlias(family, effort, ctxSuffix, thinking string) string {
	parts := []string{"clyde", family}
	if effort != "" {
		parts = append(parts, effort)
	}
	if ctxSuffix != "" {
		parts = append(parts, ctxSuffix)
	}
	if thinking != "" && thinking != ThinkingDefault {
		parts = append(parts, "thinking", thinking)
	}
	return strings.Join(parts, "-")
}

// resolveFromConfig converts a user provided AdapterModel entry into
// a ResolvedModel. Missing fields default to the claude backend so
// a minimal config stanza (just a Model name) works.
func resolveFromConfig(alias string, m config.AdapterModel) ResolvedModel {
	backend := m.Backend
	if backend == "" {
		if m.Shunt != "" {
			backend = BackendShunt
		} else {
			backend = BackendClaude
		}
	}
	out := ResolvedModel{
		Alias:       alias,
		Backend:     backend,
		ClaudeModel: m.Model,
		Context:     m.Context,
		Efforts:     m.Efforts,
		Shunt:       m.Shunt,
	}
	// User-supplied backend = "fallback" entries must carry the
	// short claude-p name in m.Model. The dispatcher reads
	// ResolvedModel.CLIAlias for the fallback path, so mirror it
	// here.
	if backend == BackendFallback {
		out.CLIAlias = m.Model
	}
	return out
}

// Resolve looks up the alias and returns the resolved model plus the
// chosen effort tier. reqEffort may be empty; the registry uses the
// alias-bound effort first, then the family default. Unknown aliases
// fall through to the registry default alias (or the configured
// fallback shunt).
//
// Returns an error when the caller asks for an effort value the
// alias's family doesn't support server-side. This surfaces as a
// 400 to the OpenAI client rather than letting Anthropic 400 with
// a less actionable message.
func (r *Registry) Resolve(alias, reqEffort string) (ResolvedModel, string, error) {
	if alias == "" {
		alias = r.def
	}
	key := strings.ToLower(alias)
	m, ok := r.models[key]
	if !ok {
		if r.fallbackShnt != "" {
			if _, sok := r.shunts[r.fallbackShnt]; sok {
				return ResolvedModel{
					Alias:   alias,
					Backend: BackendShunt,
					Shunt:   r.fallbackShnt,
				}, "", nil
			}
		}
		def, dok := r.models[strings.ToLower(r.def)]
		if !dok {
			return ResolvedModel{}, "", fmt.Errorf("unknown model %q and no usable default", alias)
		}
		m = def
	}

	effort := strings.ToLower(reqEffort)
	if effort == "" {
		if m.Effort != "" {
			effort = m.Effort
		} else if len(m.Efforts) > 0 {
			effort = m.Efforts[0]
		}
	} else {
		if len(m.Efforts) == 0 {
			return ResolvedModel{}, "", fmt.Errorf(
				"model %q does not accept effort (family declares no efforts in toml)",
				m.Alias,
			)
		}
		if !contains(m.Efforts, effort) {
			return ResolvedModel{}, "", fmt.Errorf(
				"effort %q not supported for %q (allowed: %s)",
				effort, m.Alias, strings.Join(m.Efforts, ", "),
			)
		}
	}
	return m, effort, nil
}

// Shunt returns the shunt config for a named entry.
func (r *Registry) Shunt(name string) (config.AdapterShunt, bool) {
	s, ok := r.shunts[strings.ToLower(name)]
	return s, ok
}

// List returns the resolved models for /v1/models. Order is
// undefined; callers that care should sort by Alias.
func (r *Registry) List() []ResolvedModel {
	out := make([]ResolvedModel, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}
	return out
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// ClaudeEffortFlag translates an effort tier into the string passed
// to claude -p --effort on the legacy backend. Empty input (or any
// unrecognised tier) returns empty so the caller can omit the flag.
func ClaudeEffortFlag(tier string) string {
	switch strings.ToLower(tier) {
	case EffortLow:
		return "low"
	case EffortMedium, "med":
		return "medium"
	case EffortHigh:
		return "high"
	case EffortMax:
		return "max"
	}
	return ""
}
