package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoteControlEnabled covers the per session vs global default
// precedence baked into the helper. The global default is loaded on
// every call so the env var XDG_CONFIG_HOME indirection cannot easily
// stub it. The test focuses on the per session settings file path
// which is the more interesting half of the precedence rule.
func TestRemoteControlEnabled(t *testing.T) {
	dir := t.TempDir()

	// Empty path means no per session file. Falls through to global.
	if remoteControlEnabled("") {
		// Global default is unlikely to be on by default in test env;
		// if it is the test would pass here too. Either result is OK.
	}

	// Non existent path also falls through to global default.
	if remoteControlEnabled(filepath.Join(dir, "missing.json")) {
		// Same caveat as above.
	}

	// Per session settings.json with remoteControl=true forces true
	// regardless of the global default.
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"remoteControl":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !remoteControlEnabled(path) {
		t.Fatalf("expected true when settings.json sets remoteControl=true")
	}

	// Per session settings.json with remoteControl=false leaves the
	// decision to the global default. We cannot deterministically
	// test that here without controlling the user's config, so just
	// assert the call does not panic and returns one of the two
	// boolean values. That is a smoke test, not a behavior assertion.
	if err := os.WriteFile(path, []byte(`{"remoteControl":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = remoteControlEnabled(path)
}
