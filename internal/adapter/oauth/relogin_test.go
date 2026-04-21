package oauth

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestAutoRelogin_SkipsOnNonTTY asserts that autoRelogin refuses to spawn
// `claude auth login` when stderr is not a terminal. go test runs with
// stderr pointing at the test harness (non-TTY), so the gate should fire.
func TestAutoRelogin_SkipsOnNonTTY(t *testing.T) {
	m := &Manager{credentialsDir: t.TempDir()}
	origErr := errors.New("token refresh: invalid_grant: refresh token rotated")
	err := m.autoRelogin(context.Background(), origErr)
	if err == nil {
		t.Fatalf("autoRelogin returned nil on non-TTY; want error")
	}
	if !errors.Is(err, origErr) {
		t.Fatalf("autoRelogin err %v does not wrap original %v", err, origErr)
	}
	if !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("err message %q missing 'non-interactive' hint", err.Error())
	}
	if !m.relogin.lastAttempt.IsZero() {
		t.Fatalf("autoRelogin populated lastAttempt despite skipping; got %v", m.relogin.lastAttempt)
	}
}

// TestAutoRelogin_PassthroughNonInvalidGrant ensures the gate does not
// interfere with unrelated errors.
func TestAutoRelogin_PassthroughNonInvalidGrant(t *testing.T) {
	m := &Manager{credentialsDir: t.TempDir()}
	origErr := errors.New("network timeout")
	err := m.autoRelogin(context.Background(), origErr)
	if err != origErr {
		t.Fatalf("autoRelogin returned %v; want original err unchanged", err)
	}
}
