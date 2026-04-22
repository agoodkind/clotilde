package claude_test

import (
	"os"
	"path/filepath"
	"testing"

	"goodkind.io/clyde/internal/claude"
)

func TestExtractModelAndLastTime(t *testing.T) {
	tests := []struct {
		name          string
		transcript    string
		expectedModel string
		expectTime    bool // whether we expect a non-zero timestamp
	}{
		{
			name: "returns model and timestamp from assistant entry",
			transcript: `{"type":"user","timestamp":"2025-01-01T10:00:00Z","message":{"content":"hello"}}
{"type":"assistant","timestamp":"2025-01-01T10:00:05Z","message":{"model":"claude-sonnet-4-5-20250929","content":"hi"}}`,
			expectedModel: "sonnet",
			expectTime:    true,
		},
		{
			name: "last timestamp wins even if not on assistant entry",
			transcript: `{"type":"assistant","timestamp":"2025-01-01T10:00:05Z","message":{"model":"claude-sonnet-4-5-20250929","content":"hi"}}
{"type":"user","timestamp":"2025-01-01T10:01:00Z","message":{"content":"follow-up"}}`,
			expectedModel: "sonnet",
			expectTime:    true,
		},
		{
			name: "last assistant model wins",
			transcript: `{"type":"assistant","timestamp":"2025-01-01T10:00:00Z","message":{"model":"claude-opus-4-20250514","content":"first"}}
{"type":"user","timestamp":"2025-01-01T10:00:10Z","message":{"content":"ok"}}
{"type":"assistant","timestamp":"2025-01-01T10:00:20Z","message":{"model":"claude-sonnet-4-5-20250929","content":"second"}}`,
			expectedModel: "sonnet",
			expectTime:    true,
		},
		{
			name:          "empty transcript",
			transcript:    "",
			expectedModel: "",
			expectTime:    false,
		},
		{
			name:          "no assistant entries",
			transcript:    `{"type":"user","timestamp":"2025-01-01T10:00:00Z","message":{"content":"hello"}}`,
			expectedModel: "",
			expectTime:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tt.transcript), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}

			model, ts := claude.ExtractModelAndLastTime(path)
			if model != tt.expectedModel {
				t.Errorf("model: got %q, want %q", model, tt.expectedModel)
			}
			if tt.expectTime && ts.IsZero() {
				t.Error("expected non-zero timestamp, got zero")
			}
			if !tt.expectTime && !ts.IsZero() {
				t.Errorf("expected zero timestamp, got %v", ts)
			}
		})
	}
}

func TestExtractModelAndLastTime_NonExistentFile(t *testing.T) {
	model, ts := claude.ExtractModelAndLastTime("/non/existent/path")
	if model != "" {
		t.Errorf("expected empty model, got %q", model)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time, got %v", ts)
	}
}

func TestExtractModelAndLastTime_EmptyPath(t *testing.T) {
	model, ts := claude.ExtractModelAndLastTime("")
	if model != "" {
		t.Errorf("expected empty model, got %q", model)
	}
	if !ts.IsZero() {
		t.Errorf("expected zero time, got %v", ts)
	}
}

func TestExtractRawModelAndLastTime(t *testing.T) {
	transcript := `{"type":"assistant","timestamp":"2025-01-01T10:00:00Z","message":{"model":"claude-opus-4-20250514","content":"first"}}
{"type":"user","timestamp":"2025-01-01T10:00:10Z","message":{"content":"ok"}}
{"type":"assistant","timestamp":"2025-01-01T10:00:20Z","message":{"model":"claude-sonnet-4-5-20250929","content":"second"}}`

	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte(transcript), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	model, ts := claude.ExtractRawModelAndLastTime(path)
	if model != "claude-sonnet-4-5-20250929" {
		t.Fatalf("model: got %q", model)
	}
	if ts.IsZero() {
		t.Fatal("expected non-zero timestamp, got zero")
	}
}
