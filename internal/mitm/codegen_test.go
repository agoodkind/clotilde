package mitm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateWireConstantsEmitsValidGo(t *testing.T) {
	dir := t.TempDir()
	snap := Snapshot{
		Upstream: SnapshotUpstream{Name: "codex-cli", Version: "v1", CapturedAt: "2026-04-27T20:00:00Z"},
		Body:     SnapshotBody{Type: "response.create", FieldNames: []string{"input", "model"}, IncludeKeys: []string{"reasoning.encrypted_content"}, ToolKinds: []string{"function"}},
		Constants: SnapshotConstants{
			Originator: "codex_exec",
			OpenAIBeta: "responses_websockets=2026-02-06",
		},
	}
	out, err := GenerateWireConstants(snap, CodegenOptions{
		PackageName: "codex",
		OutputDir:   dir,
		UpstreamRef: "research/codex/snapshots/v1/reference.toml",
	})
	if err != nil {
		t.Fatalf("GenerateWireConstants: %v", err)
	}
	if filepath.Base(out) != "wire_constants_gen.go" {
		t.Errorf("expected wire_constants_gen.go, got %s", out)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := string(raw)
	wantSubstrings := []string{
		"package codex",
		"DO NOT EDIT",
		"WireOriginator",
		`"codex_exec"`,
		"WireOpenAIBeta",
		`"responses_websockets=2026-02-06"`,
		"WireBodyType",
		`"response.create"`,
		"WireBodyFieldNames",
		`"reasoning.encrypted_content"`,
		`"function"`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("output missing %q\n--- body ---\n%s", want, body)
		}
	}
}

func TestGenerateWireConstantsEmptySliceProducesEmptyDeclaration(t *testing.T) {
	dir := t.TempDir()
	snap := Snapshot{
		Upstream: SnapshotUpstream{Name: "codex-cli"},
		Body:     SnapshotBody{Type: "response.create"},
	}
	out, err := GenerateWireConstants(snap, CodegenOptions{PackageName: "codex", OutputDir: dir})
	if err != nil {
		t.Fatalf("GenerateWireConstants: %v", err)
	}
	raw, _ := os.ReadFile(out)
	body := string(raw)
	if !strings.Contains(body, "WireBodyFieldNames = []string{}") {
		t.Errorf("expected empty slice declaration, got\n%s", body)
	}
}

func TestGenerateWireConstantsRequiresPackage(t *testing.T) {
	_, err := GenerateWireConstants(Snapshot{}, CodegenOptions{})
	if err == nil {
		t.Fatal("expected error when PackageName empty")
	}
}
