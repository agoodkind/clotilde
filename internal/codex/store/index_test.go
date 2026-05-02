package codexstore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSessionIndexLatestNameWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session_index.jsonl")
	body := `{"id":"thread-1","thread_name":"old","updated_at":"2026-05-02T17:00:00Z"}` + "\n" +
		`not-json` + "\n" +
		`{"id":"thread-2","thread_name":"other","updated_at":"2026-05-02T17:01:00Z"}` + "\n" +
		`{"id":"thread-1","thread_name":"new","updated_at":"2026-05-02T17:02:00Z"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write index: %v", err)
	}

	idx, err := ReadSessionIndex(path)
	if err != nil {
		t.Fatalf("ReadSessionIndex returned error: %v", err)
	}
	if got := idx.ThreadName("thread-1"); got != "new" {
		t.Fatalf("ThreadName = %q, want new", got)
	}
}
