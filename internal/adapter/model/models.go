package model

import (
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"goodkind.io/clyde/internal/config"
)

// Backend names the kind of worker that fulfils a request.
const (
	BackendClaude = "claude"
	BackendShunt  = "shunt"
	// BackendAnthropic routes the request directly at the configured
	// messages URL. Auth uses the token from the keychain (the
	// internal/adapter/oauth package handles credentials;
	// internal/adapter/anthropic handles HTTP). Selected
	// when adapter config sets direct_oauth=true; the registry
	// rewrites every BackendClaude model to BackendAnthropic at
	// construction so the HTTP layer only has to switch on this
	// single value.
	BackendAnthropic = "anthropic"
	// BackendCodex routes to ChatGPT/Codex-backed model execution.
	BackendCodex = "codex"
)

// Effort tiers (low, medium, high, max). Sent inside output_config
// on the messages API when the beta header set allows it. Whether a
// given family accepts a given tier is
// declared in the user's toml under [adapter.families.<name>.efforts].
const (
	EffortLow    = "low"
	EffortMedium = "medium"
	EffortHigh   = "high"
	EffortXHigh  = "xhigh"
	EffortMax    = "max"
)

// Thinking modes for the extended-thinking wire shape. The adapter
// translates each value into the right payload on the direct HTTP
// path. `default` means "send no thinking block"
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
	// Backend is one of BackendClaude / BackendShunt / BackendAnthropic / BackendCodex.
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
	// derive the budget_tokens for ThinkingEnabled (budget = max-1).
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
	// FamilySlug is the cfg.Families key this alias was generated
	// from. Empty for user-supplied [adapter.models.<name>] entries
	// (which carry their own backend/model directly).
	FamilySlug string
}

// Registry owns the resolved model table used by the HTTP layer.
// Construction layers user-supplied per-alias overrides on top of
// the family-expanded aliases. There are no compiled-in defaults:
// NewRegistry rejects an AdapterConfig that omits families,
// client_identity fields, or default_model.
type Registry struct {
	models             map[string]ResolvedModel
	shunts             map[string]config.AdapterShunt
	def                string
	fallbackShnt       string
	codexEnabled       bool
	codexPrefix        []string
	codexNativeRouting string
	codexNativeShunt   string
}

// NewRegistry builds the registry from a loaded AdapterConfig. It
// returns an error when the config is incomplete: no families,
// no default model, or any required client_identity / oauth field
// empty. Callers should refuse to start the listener and surface the
// error so the user sees what is missing.
func NewRegistry(cfg config.AdapterConfig) (*Registry, error) {
	if cfg.DefaultModel == "" {
		return nil, fmt.Errorf("adapter: default_model must be set in [adapter]")
	}
	// beta_header and user_agent are optional. When empty, the
	// adapter falls through to the captured WireFlavor in
	// internal/adapter/anthropic/wire_flavors_gen.go (CLYDE-124).
	// Setting either is an explicit operator override.
	if cfg.ClientIdentity.SystemPromptPrefix == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].system_prompt_prefix must be set")
	}
	if cfg.ClientIdentity.StainlessPackageVersion == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].stainless_package_version must be set")
	}
	if cfg.ClientIdentity.StainlessRuntime == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].stainless_runtime must be set")
	}
	if cfg.ClientIdentity.StainlessRuntimeVersion == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].stainless_runtime_version must be set")
	}
	if cfg.ClientIdentity.CCVersion == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].cc_version must be set")
	}
	if cfg.ClientIdentity.CCEntrypoint == "" {
		return nil, fmt.Errorf("adapter: [adapter.client_identity].cc_entrypoint must be set")
	}
	if cfg.DirectOAuth {
		if err := cfg.OAuth.ValidateOAuthFields(); err != nil {
			return nil, err
		}
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
	if cfg.Fallback.Enabled {
		return nil, fmt.Errorf("adapter: [adapter.fallback] is no longer supported")
	}

	models := map[string]ResolvedModel{}
	for slug, family := range cfg.Families {
		if family.Model == "" {
			return nil, fmt.Errorf("adapter: family %q missing model id", slug)
		}
		if len(family.Contexts) == 0 {
			return nil, fmt.Errorf("adapter: family %q missing contexts", slug)
		}
		generateFamilyAliases(models, slug, family)
	}

	r := &Registry{
		models:             models,
		shunts:             map[string]config.AdapterShunt{},
		def:                cfg.DefaultModel,
		fallbackShnt:       strings.ToLower(cfg.FallbackShunt),
		codexEnabled:       cfg.Codex.Enabled,
		codexPrefix:        append([]string(nil), cfg.Codex.ModelPrefixes...),
		codexNativeRouting: strings.ToLower(strings.TrimSpace(cfg.Codex.NativeModelRouting)),
		codexNativeShunt:   strings.ToLower(strings.TrimSpace(cfg.Codex.NativeModelShunt)),
	}
	if r.codexNativeRouting == "" {
		if r.codexEnabled {
			r.codexNativeRouting = "codex"
		} else {
			r.codexNativeRouting = "off"
		}
	}
	switch r.codexNativeRouting {
	case "off", "codex", "shunt":
	default:
		return nil, fmt.Errorf("adapter: [adapter.codex].native_model_routing must be one of: off, codex, shunt")
	}
	if r.codexNativeRouting == "shunt" {
		if r.codexNativeShunt == "" {
			return nil, fmt.Errorf("adapter: [adapter.codex].native_model_shunt is required when native_model_routing = \"shunt\"")
		}
		if _, ok := cfg.Shunts[r.codexNativeShunt]; !ok {
			return nil, fmt.Errorf("adapter: [adapter.codex].native_model_shunt %q not found in [adapter.shunts]", r.codexNativeShunt)
		}
	}
	if len(r.codexPrefix) == 0 {
		r.codexPrefix = []string{"gpt-", "o"}
	}

	for name, m := range cfg.Models {
		if strings.EqualFold(strings.TrimSpace(m.Backend), "fallback") {
			return nil, fmt.Errorf("adapter: [adapter.models.%s].backend = %q is no longer supported", name, m.Backend)
		}
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
			"subcomponent", "adapter",
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
		"subcomponent", "adapter",
		"families", len(cfg.Families),
		"tools_capable", toolsCapable,
		"vision_capable", visionCapable,
	)

	return r, nil
}

// validateAdapterLogprobs enforces the live Anthropic logprobs policy and
// rejects the removed fallback knob so unsupported config does not go silent.
func validateAdapterLogprobs(lp config.AdapterLogprobs) error {
	if lp.Anthropic == "" && lp.Fallback == "" {
		return nil
	}
	if lp.Fallback != "" {
		return fmt.Errorf("adapter: [adapter.logprobs].fallback is no longer supported")
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
func generateFamilyAliases(out map[string]ResolvedModel, slug string, f config.AdapterFamily) {
	efforts := []string{""} // always emit a plain alias so callers can choose per-request effort
	efforts = append(efforts, f.Efforts...)
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
					FamilySlug:      slug,
				}
			}
		}
	}
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
	return out
}

func ResolveFromConfig(alias string, m config.AdapterModel) ResolvedModel {
	return resolveFromConfig(alias, m)
}

func (r *Registry) looksLikeCodexModel(alias string) bool {
	if !r.codexEnabled {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" {
		return false
	}
	if strings.HasPrefix(key, "clyde-o1") ||
		strings.HasPrefix(key, "clyde-o3") ||
		strings.HasPrefix(key, "clyde-o4") ||
		strings.HasPrefix(key, "clyde-codex-") {
		return true
	}
	return r.looksLikeNativeCodexModel(alias)
}

func (r *Registry) looksLikeNativeCodexModel(alias string) bool {
	if !r.codexEnabled {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(alias))
	if key == "" || strings.HasPrefix(key, "clyde-") {
		return false
	}
	for _, p := range r.codexPrefix {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func normalizeCodexModelAlias(alias string) string {
	key := strings.TrimSpace(alias)
	lower := strings.ToLower(key)
	for _, prefix := range []string{"clyde-codex-"} {
		if strings.HasPrefix(lower, prefix) {
			key = key[len(prefix):]
			lower = strings.ToLower(key)
			break
		}
	}
	for _, suffix := range []string{"-low", "-medium", "-high", "-xhigh"} {
		if strings.HasSuffix(lower, suffix) {
			key = key[:len(key)-len(suffix)]
			lower = strings.ToLower(key)
			break
		}
	}
	if strings.HasSuffix(lower, "-1m") {
		key = key[:len(key)-len("-1m")]
	}
	switch {
	default:
		return key
	}
}

func codexAliasEffort(alias string) string {
	lower := strings.ToLower(strings.TrimSpace(alias))
	for _, suffix := range []string{"xhigh", "high", "medium", "low"} {
		if strings.HasSuffix(lower, "-"+suffix) {
			return suffix
		}
	}
	return ""
}

func codexAliasContext(alias string) int {
	key := normalizeCodexModelAlias(alias)
	original := strings.ToLower(strings.TrimSpace(alias))
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "gpt-5.4", "gpt-5.5":
		return 1000000
	}
	if strings.HasSuffix(original, "-1m") || strings.Contains(original, "-1m-") {
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "gpt-5.4", "gpt-5.5":
			return 1000000
		}
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(key)), "gpt-5.3") {
		return 272000
	}
	return 0
}

// Resolve looks up the alias and returns the resolved model plus the
// chosen effort tier. reqEffort may be empty; the registry uses the
// alias-bound effort first, then the family default. Unknown aliases
// fall through to the registry default alias (or the configured
// fallback shunt).
//
// Returns an error when the caller asks for an effort value the
// alias's family doesn't support server-side. This surfaces as a
// 400 to the OpenAI client rather than letting the upstream return
// a less actionable message.
func (r *Registry) Resolve(alias, reqEffort string) (ResolvedModel, string, error) {
	if alias == "" {
		alias = r.def
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(alias)), "clyde-gpt-") {
		return ResolvedModel{}, "", fmt.Errorf(
			"model %q is no longer supported; use Cursor's native GPT model IDs such as gpt-5.4",
			alias,
		)
	}
	if r.looksLikeNativeCodexModel(alias) {
		switch r.codexNativeRouting {
		case "codex":
			effort := strings.ToLower(strings.TrimSpace(reqEffort))
			if effort == "" {
				effort = codexAliasEffort(alias)
			}
			return ResolvedModel{
				Alias:       alias,
				Backend:     BackendCodex,
				ClaudeModel: normalizeCodexModelAlias(alias),
				Context:     codexAliasContext(alias),
			}, effort, nil
		case "shunt":
			if _, ok := r.shunts[r.codexNativeShunt]; ok {
				return ResolvedModel{
					Alias:   alias,
					Backend: BackendShunt,
					Shunt:   r.codexNativeShunt,
				}, "", nil
			}
			return ResolvedModel{}, "", fmt.Errorf("native model shunt %q is not configured", r.codexNativeShunt)
		default:
			return ResolvedModel{}, "", fmt.Errorf(
				"unknown model %q (native model routing is off; configure [adapter.models.%q] or [adapter.codex].native_model_routing)",
				alias,
				alias,
			)
		}
	}
	if r.looksLikeCodexModel(alias) {
		effort := strings.ToLower(strings.TrimSpace(reqEffort))
		if effort == "" {
			effort = codexAliasEffort(alias)
		}
		return ResolvedModel{
			Alias:       alias,
			Backend:     BackendCodex,
			ClaudeModel: normalizeCodexModelAlias(alias),
			Context:     codexAliasContext(alias),
		}, effort, nil
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
	seen := make(map[string]struct{}, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
		seen[strings.ToLower(strings.TrimSpace(m.Alias))] = struct{}{}
	}
	if r.codexEnabled && r.codexNativeRouting == "codex" {
		for _, alias := range []string{"gpt-5.4", "gpt-5.5", "gpt-5.3-codex", "gpt-5.3-codex-spark", "o3"} {
			if _, ok := seen[alias]; ok {
				continue
			}
			out = append(out, ResolvedModel{
				Alias:       alias,
				Backend:     BackendCodex,
				ClaudeModel: normalizeCodexModelAlias(alias),
				Context:     codexAliasContext(alias),
				Efforts:     []string{EffortLow, EffortMedium, EffortHigh, EffortXHigh},
			})
		}
	}
	return out
}

func (r *Registry) Models() map[string]ResolvedModel {
	out := make(map[string]ResolvedModel, len(r.models))
	maps.Copy(out, r.models)
	return out
}

func contains(s []string, v string) bool {
	return slices.Contains(s, v)
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
