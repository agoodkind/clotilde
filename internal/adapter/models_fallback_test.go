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
