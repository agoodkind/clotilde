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

// baseConfig returns an AdapterConfig that passes the live NewRegistry
// validation for family expansion and model resolution tests.
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

func TestNewRegistryRejectsEnabledFallbackConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.Fallback = config.AdapterFallback{Enabled: true}
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatal("expected enabled fallback config to be rejected")
	} else if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRegistryRejectsFallbackLogprobsConfig(t *testing.T) {
	cfg := baseConfig()
	cfg.Logprobs.Fallback = "reject"
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatal("expected fallback logprobs config to be rejected")
	} else if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("err = %v", err)
	}
}

func TestNewRegistryRejectsFallbackModelBackend(t *testing.T) {
	cfg := baseConfig()
	cfg.Models = map[string]config.AdapterModel{
		"legacy-fallback": {
			Backend: "fallback",
			Model:   "opus",
		},
	}
	if _, err := NewRegistry(cfg); err == nil {
		t.Fatal("expected fallback backend model to be rejected")
	} else if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("err = %v", err)
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
	cfg.Codex.NativeModelRouting = "codex"
	return cfg
}

func TestResolveRoutesCodexModelPrefixes(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	cfg.Codex.NativeModelRouting = "codex"
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

func TestResolveRoutesNativeCodexByDefaultWhenCodexEnabled(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	cfg.Codex.ModelPrefixes = []string{"gpt-", "o"}
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	m, effort, err := r.Resolve("gpt-5.4", "medium")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
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
	if effort != "medium" {
		t.Fatalf("effort = %q want medium", effort)
	}
}

func TestResolveRejectsNativeCodexWhenRoutingExplicitlyOff(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	cfg.Codex.NativeModelRouting = "off"
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, _, err := r.Resolve("gpt-5.4", ""); err == nil {
		t.Fatalf("expected native gpt alias to be rejected when routing is off")
	} else if !strings.Contains(err.Error(), "native model routing is off") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListIncludesNativeCodexModelsWhenRoutable(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	var gpt54 *ResolvedModel
	for _, m := range r.List() {
		if m.Alias == "gpt-5.4" {
			copy := m
			gpt54 = &copy
			break
		}
	}
	if gpt54 == nil {
		t.Fatalf("gpt-5.4 missing from model list")
	}
	if gpt54.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", gpt54.Backend, BackendCodex)
	}
	if gpt54.Context != 1000000 {
		t.Fatalf("Context = %d want 1000000", gpt54.Context)
	}
}

func TestResolveRoutesClydeCodexAliases(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	cfg.Codex.NativeModelRouting = "codex"
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	m, _, err := r.Resolve("clyde-codex-gpt-5.4", "")
	if err != nil {
		t.Fatalf("Resolve clyde-codex-gpt: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.4" {
		t.Fatalf("ClaudeModel = %q want gpt-5.4", m.ClaudeModel)
	}

	m, _, err = r.Resolve("gpt-5.3-codex-spark", "")
	if err != nil {
		t.Fatalf("Resolve gpt-5.3-codex-spark: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.3-codex-spark" {
		t.Fatalf("ClaudeModel = %q want gpt-5.3-codex-spark", m.ClaudeModel)
	}

	m, effort, err := r.Resolve("gpt-5.4", "xhigh")
	if err != nil {
		t.Fatalf("Resolve gpt-5.4 xhigh: %v", err)
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
	if effort != "xhigh" {
		t.Fatalf("effort = %q want xhigh", effort)
	}

	m, effort, err = r.Resolve("gpt-5.5", "high")
	if err != nil {
		t.Fatalf("Resolve gpt-5.5 high: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.5" {
		t.Fatalf("ClaudeModel = %q want gpt-5.5", m.ClaudeModel)
	}
	if m.Context != 1000000 {
		t.Fatalf("Context = %d want 1000000", m.Context)
	}
	if effort != "high" {
		t.Fatalf("effort = %q want high", effort)
	}

	m, effort, err = r.Resolve("gpt-5.3-codex", "medium")
	if err != nil {
		t.Fatalf("Resolve gpt-5.3-codex medium: %v", err)
	}
	if m.Backend != BackendCodex {
		t.Fatalf("backend = %q want %q", m.Backend, BackendCodex)
	}
	if m.ClaudeModel != "gpt-5.3-codex" {
		t.Fatalf("ClaudeModel = %q want gpt-5.3-codex", m.ClaudeModel)
	}
	if m.Context != 272000 {
		t.Fatalf("Context = %d want 272000", m.Context)
	}
	if effort != "medium" {
		t.Fatalf("effort = %q want medium", effort)
	}
}

func TestResolveRejectsRemovedClydeGPTAliases(t *testing.T) {
	cfg := baseConfig()
	cfg.Codex.Enabled = true
	r, err := NewRegistry(cfg)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	if _, _, err := r.Resolve("clyde-gpt-5.4-1m-high", ""); err == nil {
		t.Fatalf("expected clyde-gpt alias to be rejected")
	} else if !strings.Contains(err.Error(), "no longer supported") {
		t.Fatalf("unexpected error: %v", err)
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
			BetaHeader:              "beta",
			UserAgent:               "ua",
			SystemPromptPrefix:      "prefix",
			StainlessPackageVersion: "1",
			StainlessRuntime:        "go",
			StainlessRuntimeVersion: "1",
			CCVersion:               "1",
			CCEntrypoint:            "entry",
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
		alias              string
		reqEffort          string
		wantBackend        string
		wantModel          string
		wantContext        int
		wantEffort         string
		wantThinking       string
		wantSupportsTools  bool
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
			alias:       "gpt-5.4",
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.4",
			wantContext: 1000000,
		},
		{
			alias:       "gpt-5.5",
			reqEffort:   EffortMedium,
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.5",
			wantContext: 1000000,
			wantEffort:  EffortMedium,
		},
		{
			alias:       "gpt-5.5",
			reqEffort:   EffortXHigh,
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.5",
			wantContext: 1000000,
			wantEffort:  EffortXHigh,
		},
		{
			alias:       "gpt-5.4-mini",
			reqEffort:   EffortLow,
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.4-mini",
			wantEffort:  EffortLow,
		},
		{
			alias:       "gpt-5.3-codex",
			reqEffort:   EffortHigh,
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.3-codex",
			wantContext: 272000,
			wantEffort:  EffortHigh,
		},
		{
			alias:       "gpt-5.3-codex-spark",
			reqEffort:   EffortXHigh,
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.3-codex-spark",
			wantContext: 272000,
			wantEffort:  EffortXHigh,
		},
		{
			alias:       "gpt-5.2",
			wantBackend: BackendCodex,
			wantModel:   "gpt-5.2",
		},
		{
			alias:       "o3-high",
			wantBackend: BackendCodex,
			wantModel:   "o3",
			wantEffort:  EffortHigh,
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
		m, ok := r.Models()[tc.alias]
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
