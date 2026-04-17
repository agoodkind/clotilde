package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestBackupTranscript verifies the backup writes a byte-identical copy
// under the session-scoped backups directory.
func TestBackupTranscript(t *testing.T) {
	tmp := t.TempDir()
	// Point XDG_DATA_HOME at a temp dir so backup lands under our control.
	t.Setenv("XDG_DATA_HOME", tmp)

	src := filepath.Join(tmp, "source.jsonl")
	content := `{"type":"user","message":{"content":"hi"}}` + "\n"
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	bk, err := BackupTranscript(src, "my-session")
	if err != nil {
		t.Fatalf("BackupTranscript: %v", err)
	}
	if bk.Path == "" {
		t.Fatal("empty backup path")
	}
	if bk.Bytes != int64(len(content)) {
		t.Errorf("bytes=%d want %d", bk.Bytes, len(content))
	}

	// Destination must live under the expected XDG layout.
	want := filepath.Join(tmp, "clotilde", "backups", "my-session")
	if !strings.HasPrefix(bk.Path, want) {
		t.Errorf("backup path %q does not start with %q", bk.Path, want)
	}

	got, err := os.ReadFile(bk.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("backup content mismatch: got %q want %q", got, content)
	}
}

// TestBackupTranscript_Prunes verifies the session backup dir is capped
// at MaxBackupsPerSession on each new backup.
func TestBackupTranscript_Prunes(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_DATA_HOME", tmp)

	src := filepath.Join(tmp, "t.jsonl")
	if err := os.WriteFile(src, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Shrink the cap for the test.
	prev := MaxBackupsPerSession
	MaxBackupsPerSession = 3
	defer func() { MaxBackupsPerSession = prev }()

	// Take 5 backups with a small sleep so their millisecond-resolution
	// timestamps (and thus filenames) differ.
	dir := filepath.Join(tmp, "clotilde", "backups", "sess")
	for i := 0; i < 5; i++ {
		if _, err := BackupTranscript(src, "sess"); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		time.Sleep(5 * time.Millisecond)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("expected 3 backups retained, got %d", len(entries))
	}
}
