package claude_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/fgrehm/clotilde/internal/claude"
)

func TestExtractLastModel(t *testing.T) {
	tests := []struct {
		name           string
		transcriptData string
		expectedModel  string
	}{
		{
			name: "sonnet model",
			transcriptData: `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","role":"assistant","content":"hi"}}`,
			expectedModel: "sonnet",
		},
		{
			name: "opus model",
			transcriptData: `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-opus-4-20250514","role":"assistant","content":"hi"}}`,
			expectedModel: "opus",
		},
		{
			name: "haiku model",
			transcriptData: `{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-3-5-haiku-20250219","role":"assistant","content":"hi"}}`,
			expectedModel: "haiku",
		},
		{
			name: "multiple messages - returns last",
			transcriptData: `{"type":"assistant","message":{"model":"claude-opus-4-20250514","role":"assistant","content":"first"}}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","role":"assistant","content":"second"}}`,
			expectedModel: "sonnet",
		},
		{
			name:           "no assistant messages",
			transcriptData: `{"type":"user","message":{"role":"user","content":"hello"}}`,
			expectedModel:  "",
		},
		{
			name:           "empty transcript",
			transcriptData: "",
			expectedModel:  "",
		},
		{
			name: "mixed entries with non-assistant types",
			transcriptData: `{"type":"file-history-snapshot","messageId":"abc"}
{"type":"user","message":{"role":"user","content":"hello"}}
{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","role":"assistant","content":"hi"}}
{"type":"summary","summary":"test"}`,
			expectedModel: "sonnet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temp file with transcript data
			tmpDir := t.TempDir()
			transcriptPath := filepath.Join(tmpDir, "transcript.jsonl")

			if err := os.WriteFile(transcriptPath, []byte(tt.transcriptData), 0o644); err != nil {
				t.Fatalf("Failed to write test transcript: %v", err)
			}

			result := claude.ExtractLastModel(transcriptPath)
			if result != tt.expectedModel {
				t.Errorf("Expected model %q, got %q", tt.expectedModel, result)
			}
		})
	}
}

func TestExtractLastModel_LargeFile(t *testing.T) {
	// Verify that the tail-read optimization finds the last model in a file > 128KB.
	// An early "opus" message is buried before the 128KB tail;
	// a later "sonnet" message sits in the tail.
	tmpDir := t.TempDir()
	transcriptPath := filepath.Join(tmpDir, "large.jsonl")

	f, err := os.Create(transcriptPath)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	writeLine := func(line string) {
		t.Helper()
		if _, err := fmt.Fprintln(f, line); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	writeLine(`{"type":"assistant","message":{"model":"claude-opus-4-20250514","role":"assistant","content":"early"}}`)

	// 2500 lines × ~63 bytes ≈ 157KB – pushes the opus entry before the 128KB tail window.
	userLine := `{"type":"user","message":{"role":"user","content":"padding"}}`
	for range 2500 {
		writeLine(userLine)
	}

	writeLine(`{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","role":"assistant","content":"last"}}`)

	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	result := claude.ExtractLastModel(transcriptPath)
	if result != "sonnet" {
		t.Errorf("got %q, want %q", result, "sonnet")
	}
}

func TestExtractLastModel_NonExistentFile(t *testing.T) {
	result := claude.ExtractLastModel("/non/existent/path")
	if result != "" {
		t.Errorf("Expected empty string for non-existent file, got %q", result)
	}
}

func TestExtractLastModel_EmptyPath(t *testing.T) {
	result := claude.ExtractLastModel("")
	if result != "" {
		t.Errorf("Expected empty string for empty path, got %q", result)
	}
}

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
