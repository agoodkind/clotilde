package adapter

import (
	"strings"
	"testing"

	"goodkind.io/clyde/internal/config"
)

func validClientIdentity() config.AdapterClientIdentity {
	return config.AdapterClientIdentity{
		BetaHeader:              "x",
		UserAgent:               "y",
		SystemPromptPrefix:      "z",
		StainlessPackageVersion: "0",
		StainlessRuntime:        "node",
		StainlessRuntimeVersion: "v0",
		CCVersion:               "1.0.0",
		CCEntrypoint:            "ci",
	}
}

// baseConfig returns an AdapterConfig that passes the
// non-fallback portion of NewRegistry validation. Tests layer
// AdapterFallback fields on top to exercise the new validator.
func baseConfig() config.AdapterConfig {
	return config.AdapterConfig{
		DefaultModel:   "clyde-haiku-4-5",
		ClientIdentity: validClientIdentity(),
		Families: map[string]config.AdapterFamily{
			"haiku-4-5": {
				Model:           "claude-haiku-4-5-20251001",
				ThinkingModes:   []string{"default"},
				MaxOutputTokens: 16000,
				SupportsTools:   boolPtr(true),
				SupportsVision:  boolPtr(true),
				Contexts: []config.AdapterModelContext{
					{Tokens: 200000},
				},
			},
		},
	}
}

func TestNewRegistryDirectOAuthRequiresOAuthBlock(t *testing.T) {
	cfg := baseConfig()
	cfg.DirectOAuth = true
	_, err := NewRegistry(cfg)
	if err == nil {
		t.Fatal("expected error when direct_oauth without [adapter.oauth]")
	}
	if !strings.Contains(err.Error(), "token_url") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRegistryFallbackDisabledIsNoop(t *testing.T) {
	cfg := baseConfig()
	cfg.Fallback = config.AdapterFallback{Enabled: false}
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for alias, m := range r.models {
		if m.CLIAlias != "" {
			t.Fatalf("alias %q has CLIAlias %q; want empty when fallback disabled", alias, m.CLIAlias)
		}
	}
}

func TestNewRegistryFallbackPopulatesCLIAlias(t *testing.T) {
	cfg := baseConfig()
	cfg.Fallback = validFallback()
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, ok := r.models["clyde-haiku-4-5"]
	if !ok {
		t.Fatalf("alias missing")
	}
	if m.CLIAlias != "haiku" {
		t.Fatalf("CLIAlias = %q want haiku", m.CLIAlias)
	}
	if m.FamilySlug != "haiku-4-5" {
		t.Fatalf("FamilySlug = %q want haiku-4-5", m.FamilySlug)
	}
}

func validFallback() config.AdapterFallback {
	return config.AdapterFallback{
		Enabled:           true,
		Trigger:           FallbackTriggerOnOAuthFailure,
		Timeout:           "30s",
		MaxConcurrent:     2,
		AllowedFamilies:   []string{"haiku-4-5"},
		ScratchSubdir:     "fallback",
		FailureEscalation: FallbackEscalationFallbackError,
		CLIAliases: map[string]string{
			"haiku-4-5": "haiku",
		},
	}
}

func modelMatrixConfig() config.AdapterConfig {
	cfg := baseConfig()
	cfg.DefaultModel = "clyde-opus-4-7"
	cfg.Families["opus-4-7"] = config.AdapterFamily{
		Model:           "claude-opus-4-7",
		Efforts:         []string{EffortLow, EffortMedium, EffortHigh, EffortMax},
		ThinkingModes:   []string{ThinkingDefault, ThinkingAdaptive, ThinkingEnabled, ThinkingDisabled},
		MaxOutputTokens: 128000,
		SupportsTools:   boolPtr(true),
		SupportsVision:  boolPtr(true),
		Contexts: []config.AdapterModelContext{
			{Tokens: 200000},
			{Tokens: 1000000, AliasSuffix: "1m", WireSuffix: "[1m]"},
		},
	}
	cfg.Families["opus-4-6"] = config.AdapterFamily{
		Model:           "claude-opus-4-6",
		Efforts:         []string{EffortLow, EffortMedium, EffortHigh, EffortMax},
		ThinkingModes:   []string{ThinkingDefault, ThinkingAdaptive, ThinkingEnabled, ThinkingDisabled},
		MaxOutputTokens: 128000,
		SupportsTools:   boolPtr(true),
		SupportsVision:  boolPtr(true),
		Contexts: []config.AdapterModelContext{
			{Tokens: 200000},
			{Tokens: 1000000, AliasSuffix: "1m", WireSuffix: "[1m]"},
		},
	}
	cfg.Codex.Enabled = true
	cfg.Codex.ModelPrefixes = []string{"gpt-", "o"}
	return cfg
}

func TestNewRegistryFallbackRejectsBadTrigger(t *testing.T) {
	cases := []struct {
		name    string
		trigger string
		wantSub string
	}{
		{"empty", "", "trigger must be set"},
		{"bogus", "whenever", "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			fb := validFallback()
			fb.Trigger = tc.trigger
			cfg.Fallback = fb
			_, err := NewRegistry(cfg)
			if err == nil {
				t.Fatalf("want error for trigger %q", tc.trigger)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v; want substring %q", err, tc.wantSub)
			}
		})
	}
}

func TestNewRegistryFallbackRejectsBadTimeout(t *testing.T) {
	cases := []struct {
		name    string
		timeout string
	}{
		{"empty", ""},
		{"unparseable", "soon"},
		{"zero", "0s"},
		{"negative", "-1s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig()
			fb := validFallback()
			fb.Timeout = tc.timeout
			cfg.Fallback = fb
			if _, err := NewRegistry(cfg); err == nil {
				t.Fatalf("want error for timeout %q", tc.timeout)
			}
		})
	}
}

func TestNewRegistryFallbackRejectsZeroConcurrency(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.MaxConcurrent = 0
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for max_concurrent = 0")
	}
}

func TestNewRegistryFallbackRejectsEmptyScratchSubdir(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.ScratchSubdir = ""
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for empty scratch_subdir")
	}
}

func TestNewRegistryFallbackRejectsBadEscalation(t *testing.T) {
	cases := []string{"", "elsewhere"}
	for _, v := range cases {
		t.Run(v, func(t *testing.T) {
			cfg := baseConfig()
			fb := validFallback()
			fb.FailureEscalation = v
			cfg.Fallback = fb
			if _, err := NewRegistry(cfg); err == nil {
				t.Fatalf("want error for escalation %q", v)
			}
		})
	}
}

func TestNewRegistryFallbackRejectsUnknownFamilyInAllowedFamilies(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.AllowedFamilies = []string{"unknown-family"}
	cfg.Fallback = fb
	_, err := NewRegistry(cfg)
	if err == nil || !strings.Contains(err.Error(), "unknown family") {
		t.Fatalf("want unknown family error, got %v", err)
	}
}

func TestResolveRoutesCodexModelPrefixes(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, effort, err := r.Resolve("gpt-5.4", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.Alias != "gpt-5.4" {
		t.Fatalf("alias = %q want gpt-5.4", m.Alias)
	}
	if effort != "" {
		t.Fatalf("effort = %q want empty", effort)
	}
}

func TestResolveRoutesClydeCodexAliases(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, effort, err := r.Resolve("clyde-gpt-5.4", "")
	if err != nil {
		t.Fatalf("Resolve clyde-gpt: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.Alias != "clyde-gpt-5.4" {
		t.Fatalf("alias = %q want clyde-gpt-5.4", m.Alias)
	}
	if m.ClaudeModel != "gpt-5.4" {
		t.Fatalf("ClaudeModel = %q want gpt-5.4", m.ClaudeModel)
	}
	if effort != "" {
		t.Fatalf("effort = %q want empty", effort)
	}

	m, _, err = r.Resolve("clyde-codex-gpt-5.4", "")
	if err != nil {
		t.Fatalf("Resolve clyde-codex-gpt: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.4" {
		t.Fatalf("ClaudeModel = %q want gpt-5.4", m.ClaudeModel)
	}

	m, _, err = r.Resolve("clyde-gpt-5.3-codex-spark", "")
	if err != nil {
		t.Fatalf("Resolve clyde-gpt-5.3-codex-spark: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.3-codex-spark" {
		t.Fatalf("ClaudeModel = %q want gpt-5.3-codex-spark", m.ClaudeModel)
	}

	m, _, err = r.Resolve("clyde-gpt-5.2", "")
	if err != nil {
		t.Fatalf("Resolve clyde-gpt-5.2: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.2" {
		t.Fatalf("ClaudeModel = %q want gpt-5.2", m.ClaudeModel)
	}

	m, effort, err = r.Resolve("clyde-gpt-5.4-xhigh", "")
	if err != nil {
		t.Fatalf("Resolve clyde-gpt-5.4-xhigh: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.4" {
		t.Fatalf("ClaudeModel = %q want gpt-5.4", m.ClaudeModel)
	}
	if effort != "xhigh" {
		t.Fatalf("effort = %q want xhigh", effort)
	}

	m, effort, err = r.Resolve("clyde-gpt-5.4-1m-high", "")
	if err != nil {
		t.Fatalf("Resolve clyde-gpt-5.4-1m-high: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.4" {
		t.Fatalf("ClaudeModel = %q want gpt-5.4", m.ClaudeModel)
	}
	if m.Context != 1000000 {
		t.Fatalf("Context = %d want 1000000", m.Context)
	}
	if effort != "high" {
		t.Fatalf("effort = %q want high", effort)
	}

}

func TestResolveDoesNotRouteCodexWhenDisabled(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = false
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, _, err := r.Resolve("gpt-5.4", "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if m.Backend == BackendCodex {
		t.Fatalf("backend = %q want non-codex fallback/default", m.Backend)
	}
}

func TestResolveDoesNotRouteClydeOpusAliasesToCodex(t *testing.T) {
	r, err := NewRegistry(config.AdapterConfig{
		DefaultModel: "clyde-opus-4-7",
		ClientIdentity: config.AdapterClientIdentity{
			BetaHeader:               "beta",
			UserAgent:                "ua",
			SystemPromptPrefix:       "prefix",
			StainlessPackageVersion:  "1",
			StainlessRuntime:         "go",
			StainlessRuntimeVersion:  "1",
			CCVersion:                "1",
			CCEntrypoint:             "entry",
		},
		Families: map[string]config.AdapterFamily{
			"opus-4-7": {
				Model:           "claude-opus-4-7",
				Contexts:        []config.AdapterModelContext{{AliasSuffix: "", Tokens: 200000}},
				Efforts:         []string{"medium"},
				ThinkingModes:   []string{"", "enabled"},
				SupportsTools:   boolPtr(true),
				SupportsVision:  boolPtr(true),
				MaxOutputTokens: 8192,
			},
		},
		Codex: config.AdapterCodex{
			Enabled:       true,
			ModelPrefixes: []string{"gpt-", "o"},
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	m, _, err := r.Resolve("clyde-opus-4-7-medium-thinking-enabled", "")
	if err != nil {
		t.Fatalf("Resolve clyde-opus-4-7-medium-thinking-enabled: %v", err)
	}
	if m.Backend == BackendCodex {
		t.Fatalf("backend = %q want non-codex", m.Backend)
	}
	if m.ClaudeModel != "claude-opus-4-7" {
		t.Fatalf("ClaudeModel = %q want claude-opus-4-7", m.ClaudeModel)
	}
}

func TestResolveModelRoutingMatrix(t *testing.T) {
	r, err := NewRegistry(modelMatrixConfig())
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	cases := []struct {
		alias             string
		reqEffort         string
		wantBackend       string
		wantModel         string
		wantContext       int
		wantEffort        string
		wantThinking      string
		wantSupportsTools bool
		wantSupportsVision bool
	}{
		{
			alias:              "clyde-opus-4-7",
			wantBackend:        BackendClaude,
			wantModel:          "claude-opus-4-7",
			wantContext:        200000,
			wantEffort:         EffortLow,
			wantThinking:       ThinkingDefault,
			wantSupportsTools:  true,
			wantSupportsVision: true,
		},
		{
			alias:              "clyde-opus-4-7-medium-thinking-enabled",
			wantBackend:        BackendClaude,
			wantModel:          "claude-opus-4-7",
			wantContext:        200000,
			wantEffort:         EffortMedium,
			wantThinking:       ThinkingEnabled,
			wantSupportsTools:  true,
			wantSupportsVision: true,
		},
		{
			alias:              "clyde-opus-4-7-high-1m-thinking-enabled",
			wantBackend:        BackendClaude,
			wantModel:          "claude-opus-4-7[1m]",
			wantContext:        1000000,
			wantEffort:         EffortHigh,
			wantThinking:       ThinkingEnabled,
			wantSupportsTools:  true,
			wantSupportsVision: true,
		},
		{
			alias:              "clyde-opus-4-6-max-1m-thinking-adaptive",
			wantBackend:        BackendClaude,
			wantModel:          "claude-opus-4-6[1m]",
			wantContext:        1000000,
			wantEffort:         EffortMax,
			wantThinking:       ThinkingAdaptive,
			wantSupportsTools:  true,
			wantSupportsVision: true,
		},
		{
			alias:        "gpt-5.4",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.4",
		},
		{
			alias:        "clyde-gpt-5.4",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.4",
		},
		{
			alias:        "clyde-gpt-5.4-1m-high",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.4",
			wantContext:  1000000,
			wantEffort:   EffortHigh,
		},
		{
			alias:        "clyde-gpt-5.5-medium",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.5",
			wantEffort:   EffortMedium,
		},
		{
			alias:        "clyde-gpt-5.4-mini-low",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.4-mini",
			wantEffort:   EffortLow,
		},
		{
			alias:        "clyde-gpt-5.3-codex-high",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.3-codex",
			wantEffort:   EffortHigh,
		},
		{
			alias:        "clyde-gpt-5.3-codex-spark-xhigh",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.3-codex-spark",
			wantEffort:   "xhigh",
		},
		{
			alias:        "clyde-gpt-5.2",
			wantBackend:  BackendCodex,
			wantModel:    "gpt-5.2",
		},
		{
			alias:        "clyde-o3-high",
			wantBackend:  BackendCodex,
			wantModel:    "o3",
			wantEffort:   EffortHigh,
		},
	}

	for _, tc := range cases {
		t.Run(tc.alias, func(t *testing.T) {
			m, effort, err := r.Resolve(tc.alias, tc.reqEffort)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.alias, err)
			}
			if m.Backend != tc.wantBackend {
				t.Fatalf("backend = %q want %q", m.Backend, tc.wantBackend)
			}
			if m.ClaudeModel != tc.wantModel {
				t.Fatalf("ClaudeModel = %q want %q", m.ClaudeModel, tc.wantModel)
			}
			if m.Context != tc.wantContext {
				t.Fatalf("Context = %d want %d", m.Context, tc.wantContext)
			}
			if effort != tc.wantEffort {
				t.Fatalf("effort = %q want %q", effort, tc.wantEffort)
			}
			if tc.wantBackend == BackendClaude {
				if m.Thinking != tc.wantThinking {
					t.Fatalf("Thinking = %q want %q", m.Thinking, tc.wantThinking)
				}
				if m.SupportsTools != tc.wantSupportsTools {
					t.Fatalf("SupportsTools = %v want %v", m.SupportsTools, tc.wantSupportsTools)
				}
				if m.SupportsVision != tc.wantSupportsVision {
					t.Fatalf("SupportsVision = %v want %v", m.SupportsVision, tc.wantSupportsVision)
				}
			}
		})
	}
}

func TestNewRegistryFallbackRejectsEmptyAllowedFamilies(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.AllowedFamilies = nil
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for empty allowed_families")
	}
}

func TestNewRegistryFallbackRejectsEmptyCLIAliases(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.CLIAliases = nil
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for empty cli_aliases")
	}
}

func TestNewRegistryFallbackRejectsCLIAliasForUnknownFamily(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.CLIAliases = map[string]string{
		"unknown": "haiku",
	}
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for cli_alias on unknown family")
	}
}

func TestNewRegistryFallbackRejectsEmptyCLIName(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.CLIAliases = map[string]string{
		"haiku-4-5": "",
	}
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for empty cli_alias name")
	}
}

func TestNewRegistryFallbackRejectsAllowedFamilyWithoutCLIAlias(t *testing.T) {
	cfg := baseConfig()
	cfg.Families["sonnet-4-6"] = config.AdapterFamily{
		Model:           "claude-sonnet-4-6",
		ThinkingModes:   []string{"default"},
		MaxOutputTokens: 64000,
		SupportsTools:   boolPtr(true),
		SupportsVision:  boolPtr(true),
		Contexts: []config.AdapterModelContext{
			{Tokens: 200000},
		},
	}
	fb := validFallback()
	fb.AllowedFamilies = []string{"haiku-4-5", "sonnet-4-6"}
	// CLIAliases only covers haiku-4-5; sonnet missing.
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for missing cli_alias on whitelisted family")
	}
}

func TestNewRegistryFallbackForwardToShuntRequiresShuntName(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.ForwardToShunt = config.AdapterFallbackShunt{Enabled: true, Shunt: ""}
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for empty forward_to_shunt.shunt")
	}
}

func TestNewRegistryFallbackForwardToShuntRequiresShuntDeclared(t *testing.T) {
	cfg := baseConfig()
	fb := validFallback()
	fb.ForwardToShunt = config.AdapterFallbackShunt{Enabled: true, Shunt: "openai"}
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatalf("want error for forward_to_shunt referencing undeclared shunt")
	}
}

func TestNewRegistryFallbackForwardToShuntAcceptsDeclaredShunt(t *testing.T) {
	cfg := baseConfig()
	cfg.Shunts = map[string]config.AdapterShunt{
		"openai": {BaseURL: "http://example/v1"},
	}
	fb := validFallback()
	fb.ForwardToShunt = config.AdapterFallbackShunt{Enabled: true, Shunt: "openai"}
	cfg.Fallback = fb
	if _, err := NewRegistry(cfg); err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
}

func TestResolveFromConfigFallbackBackend(t *testing.T) {
	rm := resolveFromConfig("opus-direct", config.AdapterModel{
		Backend: BackendFallback,
		Model:   "opus",
	})
	if rm.Backend != BackendFallback {
		t.Fatalf("backend = %q want %q", rm.Backend, BackendFallback)
	}
	if rm.CLIAlias != "opus" {
		t.Fatalf("CLIAlias = %q want opus", rm.CLIAlias)
	}
}

func TestNewRegistrySupportsOpus46FamilyAliases(t *testing.T) {
	cfg := baseConfig()
	cfg.DefaultModel = "clyde-opus-4-6"
	cfg.Families["opus-4-6"] = config.AdapterFamily{
		Model:           "claude-opus-4-6",
		Efforts:         []string{EffortLow, EffortMedium, EffortHigh, EffortMax},
		ThinkingModes:   []string{ThinkingDefault, ThinkingAdaptive, ThinkingEnabled, ThinkingDisabled},
		MaxOutputTokens: 128000,
		SupportsTools:   boolPtr(true),
		SupportsVision:  boolPtr(true),
		Contexts: []config.AdapterModelContext{
			{Tokens: 200000},
			{Tokens: 1000000, AliasSuffix: "1m", WireSuffix: "[1m]"},
		},
	}

	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	cases := []struct {
		alias        string
		wantModel    string
		wantContext  int
		wantEffort   string
		wantThinking string
		wantFamily   string
	}{
		{
			alias:        "clyde-opus-4-6",
			wantModel:    "claude-opus-4-6",
			wantContext:  200000,
			wantThinking: ThinkingDefault,
			wantFamily:   "opus-4-6",
		},
		{
			alias:        "clyde-opus-4-6-1m",
			wantModel:    "claude-opus-4-6[1m]",
			wantContext:  1000000,
			wantThinking: ThinkingDefault,
			wantFamily:   "opus-4-6",
		},
		{
			alias:        "clyde-opus-4-6-max-1m-thinking-adaptive",
			wantModel:    "claude-opus-4-6[1m]",
			wantContext:  1000000,
			wantEffort:   EffortMax,
			wantThinking: ThinkingAdaptive,
			wantFamily:   "opus-4-6",
		},
	}

	for _, tc := range cases {
		m, ok := r.models[tc.alias]
		if !ok {
			t.Fatalf("alias %q missing", tc.alias)
		}
		if m.ClaudeModel != tc.wantModel {
			t.Fatalf("%s ClaudeModel = %q want %q", tc.alias, m.ClaudeModel, tc.wantModel)
		}
		if m.Context != tc.wantContext {
			t.Fatalf("%s Context = %d want %d", tc.alias, m.Context, tc.wantContext)
		}
		if m.Effort != tc.wantEffort {
			t.Fatalf("%s Effort = %q want %q", tc.alias, m.Effort, tc.wantEffort)
		}
		if m.Thinking != tc.wantThinking {
			t.Fatalf("%s Thinking = %q want %q", tc.alias, m.Thinking, tc.wantThinking)
		}
		if m.FamilySlug != tc.wantFamily {
			t.Fatalf("%s FamilySlug = %q want %q", tc.alias, m.FamilySlug, tc.wantFamily)
		}
		if !m.SupportsTools || !m.SupportsVision {
			t.Fatalf("%s capabilities = tools:%v vision:%v want true/true", tc.alias, m.SupportsTools, m.SupportsVision)
		}
	}
}

func TestNewRegistryFallbackAssignsCLIToOpus46(t *testing.T) {
	cfg := baseConfig()
	cfg.DefaultModel = "clyde-opus-4-6"
	cfg.Families["opus-4-6"] = config.AdapterFamily{
		Model:           "claude-opus-4-6",
		ThinkingModes:   []string{ThinkingDefault},
		MaxOutputTokens: 128000,
		SupportsTools:   boolPtr(true),
		SupportsVision:  boolPtr(true),
		Contexts: []config.AdapterModelContext{
			{Tokens: 200000},
		},
	}
	fb := validFallback()
	fb.AllowedFamilies = []string{"haiku-4-5", "opus-4-6"}
	fb.CLIAliases["opus-4-6"] = "opus"
	cfg.Fallback = fb

	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, ok := r.models["clyde-opus-4-6"]
	if !ok {
		t.Fatalf("alias missing")
	}
	if m.CLIAlias != "opus" {
		t.Fatalf("CLIAlias = %q want opus", m.CLIAlias)
	}
}
