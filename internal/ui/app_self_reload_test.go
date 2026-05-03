package ui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDaemonBinaryUpdateIgnoresStaleHashForCandidatePath(t *testing.T) {
	stubSelfReloadProbe(t, nil)
	path := filepath.Join(t.TempDir(), "new-clyde")
	if err := os.WriteFile(path, []byte("new-binary"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	a := &App{running: true, executableHash: "old123"}

	a.applySessionEvent(SessionEvent{
		Kind:         "CLYDE_BINARY_UPDATED",
		BinaryPath:   path,
		BinaryReason: "file_replaced",
		BinaryHash:   "stale1",
	})

	if !a.running {
		t.Fatalf("stale daemon binary update should not stop the app")
	}
	if a.reloadExecPath != "" {
		t.Fatalf("reloadExecPath=%q want empty", a.reloadExecPath)
	}
}
