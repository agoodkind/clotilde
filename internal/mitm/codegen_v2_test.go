package mitm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWireFlavorsEmitsTypedFlavors(t *testing.T) {
	dir := t.TempDir()
	snap := SnapshotV2{
		Upstream: V2Upstream{
			Name:        "claude-code",
			CapturedAt:  "2026-04-28T05:11:21Z",
			RecordCount: 40,
		},
		Flavors: []FlavorShape{
			{
				Slug:        "claude-code-interactive-6fde33e0",
				RecordCount: 38,
				Methods:     []string{"POST"},
				Paths:       []string{"/v1/messages"},
				Signature: V2Signature{
					UserAgent:       "claude-cli/2.1.121 (external, cli)",
					BetaFingerprint: "advisor-tool-2026-03-01,claude-code-20250219",
					BodyKeys:        []string{"messages", "model", "system"},
				},
				Headers: []V2Header{
					{Name: "user-agent", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"claude-cli/2.1.121 (external, cli)"}, OccurrenceRate: 1.0},
					{Name: "anthropic-beta", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"claude-code-20250219,oauth-2025-04-20"}, OccurrenceRate: 1.0},
					{Name: "anthropic-version", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"2023-06-01"}, OccurrenceRate: 1.0},
					{Name: "x-app", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"cli"}, OccurrenceRate: 1.0},
					{Name: "x-stainless-arch", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"arm64"}, OccurrenceRate: 1.0},
					{Name: "authorization", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"<redacted>"}, OccurrenceRate: 1.0},
					{Name: "x-claude-code-session-id", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"abc-123"}, OccurrenceRate: 1.0},
				},
				Body: V2Body{
					Fields: []V2Field{
						{Name: "model", Kind: V2FieldKindString, Presence: V2HeaderPresenceRequired, OccurrenceRate: 1.0},
						{Name: "stream", Kind: V2FieldKindBool, Presence: V2HeaderPresenceOptional, OccurrenceRate: 0.5},
					},
				},
			},
			{
				Slug:        "claude-code-probe-f339f6a6",
				RecordCount: 2,
				Signature: V2Signature{
					UserAgent: "claude-cli/2.1.121 (external, cli)",
					BodyKeys:  []string{"messages", "model"},
				},
				Headers: []V2Header{
					{Name: "user-agent", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"claude-cli/2.1.121 (external, cli)"}, OccurrenceRate: 1.0},
					{Name: "anthropic-beta", Classification: V2HeaderClassConstant, Presence: V2HeaderPresenceRequired, ObservedValues: []string{"oauth-2025-04-20"}, OccurrenceRate: 1.0},
				},
			},
		},
	}

	out, err := GenerateWireFlavors(snap, CodegenOptions{
		PackageName: "anthropic",
		OutputDir:   dir,
		UpstreamRef: "research/claude-code/snapshots/latest/reference.toml",
	})
	if err != nil {
		t.Fatalf("GenerateWireFlavors: %v", err)
	}
	if filepath.Base(out) != "wire_flavors_gen.go" {
		t.Errorf("expected wire_flavors_gen.go, got %s", out)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	want := []string{
		"package anthropic",
		"DO NOT EDIT",
		"type WireHeader struct",
		"type WireFlavor struct",
		"WireFlavorClaudeCodeInteractive",
		"WireFlavorClaudeCodeProbe",
		"var WireFlavors = []WireFlavor{",
		`"claude-cli/2.1.121 (external, cli)"`,
		`"2023-06-01"`,
		`"claude-code-20250219,oauth-2025-04-20"`,
		`"claude-code-20250219"`,
		`"oauth-2025-04-20"`,
		`Name: "X-App", Value: "cli"`,
		`Name: "X-Stainless-Arch", Value: "arm64"`,
		`BodyFieldsRequired: []string{`,
		`"model"`,
	}
	for _, sub := range want {
		if !strings.Contains(body, sub) {
			t.Errorf("generated file missing %q", sub)
		}
	}

	// Sensitive headers must NOT appear in StaticHeaders.
	denylist := []string{
		`Name: "Authorization"`,
		`Name: "X-Claude-Code-Session-Id"`,
		`Name: "Content-Length"`,
	}
	for _, sub := range denylist {
		if strings.Contains(body, sub) {
			t.Errorf("generated file unexpectedly emits %q (must be excluded from StaticHeaders)", sub)
		}
	}
}

func TestFlavorVarNameStripsHexFingerprint(t *testing.T) {
	cases := map[string]string{
		"claude-code-interactive-6fde33e0": "WireFlavorClaudeCodeInteractive",
		"claude-code-probe-f339f6a6":       "WireFlavorClaudeCodeProbe",
		"codex-cli-default-deadbeef":       "WireFlavorCodexCliDefault",
		"single":                           "WireFlavorSingle",
	}
	for slug, want := range cases {
		got := flavorVarName(slug)
		if got != want {
			t.Errorf("flavorVarName(%q) = %q, want %q", slug, got, want)
		}
	}
}
