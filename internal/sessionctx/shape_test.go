package sessionctx_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/sessionctx"
)

// stableCategoryNames lists the known category labels Claude emits
// from collectContextData. An upstream rename should trip this test
// so calibration and preview do not silently pick up a wrong bucket.
// Deferred variants are the parent name plus " (deferred)".
var stableCategoryNames = map[string]bool{
	"System prompt":           true,
	"System prompt (deferred)": true,
	"System tools":            true,
	"System tools (deferred)": true,
	"MCP tools":               true,
	"MCP tools (deferred)":    true,
	"Memory files":            true,
	"Skills":                  true,
	"Custom agents":           true,
	"Messages":                true,
	"Compact buffer":          true,
	"Free space":              true,
}

// TestUsageShape_FromRealProbe decodes a real ContextData captured
// from Claude's get_context_usage control response and asserts the
// structural invariants the layer relies on.
func TestUsageShape_FromRealProbe(t *testing.T) {
	path := filepath.Join("testdata", "context_response.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	var raw compact.ContextUsage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal ContextUsage: %v", err)
	}

	if raw.TotalTokens <= 0 {
		t.Fatalf("totalTokens should be positive, got %d", raw.TotalTokens)
	}
	if raw.MaxTokens <= 0 {
		t.Fatalf("maxTokens should be positive, got %d", raw.MaxTokens)
	}
	if len(raw.Categories) == 0 {
		t.Fatalf("categories should not be empty")
	}

	for i, cat := range raw.Categories {
		if cat.Name == "" {
			t.Fatalf("category[%d] has empty name", i)
		}
		if !stableCategoryNames[cat.Name] {
			t.Fatalf("category[%d] name %q is not in the stable whitelist; upstream may have renamed", i, cat.Name)
		}
	}

	usage := sessionctx.Usage{ContextUsage: raw}
	if usage.StaticOverhead() <= 0 {
		t.Fatalf("StaticOverhead should be positive, got %d", usage.StaticOverhead())
	}
	if usage.TailTokens() <= 0 {
		t.Fatalf("TailTokens (Messages) should be positive, got %d", usage.TailTokens())
	}
	if usage.CategoryTokens("NonExistent") != 0 {
		t.Fatalf("CategoryTokens for missing name should return 0")
	}
}

// TestStaticOverhead_ExcludesMessageAndReserved asserts that the
// subtraction rules match the planner's contract: Messages, Compact
// buffer, and Free space are never counted as static overhead, while
// System prompt, System tools, MCP tools (and deferred), Memory
// files, and Skills always are.
func TestStaticOverhead_ExcludesMessageAndReserved(t *testing.T) {
	raw := compact.ContextUsage{
		TotalTokens: 100,
		MaxTokens:   1000,
		Categories: []compact.ContextCategory{
			{Name: "System prompt", Tokens: 100},
			{Name: "System tools", Tokens: 200},
			{Name: "Memory files", Tokens: 50},
			{Name: "Skills", Tokens: 25},
			{Name: "Messages", Tokens: 500},
			{Name: "Compact buffer", Tokens: 10},
			{Name: "Free space", Tokens: 5},
		},
	}
	usage := sessionctx.Usage{ContextUsage: raw}
	want := 100 + 200 + 50 + 25
	if got := usage.StaticOverhead(); got != want {
		t.Fatalf("StaticOverhead = %d, want %d", got, want)
	}
	if got := usage.TailTokens(); got != 500 {
		t.Fatalf("TailTokens = %d, want 500", got)
	}
	if got := usage.CategoryTokens("Compact buffer"); got != 10 {
		t.Fatalf("CategoryTokens(Compact buffer) = %d, want 10", got)
	}
}
