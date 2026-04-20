// Auto-relogin: shells out to `claude auth login` when token refresh
// fails with invalid_grant. Concurrent callers coalesce into a single
// child process via per-process singleflight + cross-process flock.
package oauth

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

// reloginMinInterval throttles auto-relogin attempts to one per
// window. Without this, a tight retry loop in the caller could spawn
// `claude auth login` repeatedly and pop a browser window every time.
const reloginMinInterval = 60 * time.Second

// reloginBreakerThreshold is how many consecutive failures trip the
// circuit breaker. Once tripped, further attempts return the original
// error with a hint until a successful relogin resets the count.
const reloginBreakerThreshold = 3

// reloginTimeout caps how long we wait for `claude auth login` to
// finish (browser-based PKCE flow, user interaction included).
const reloginTimeout = 5 * time.Minute

// reloginState tracks rate-limit and breaker state across calls.
// Held inside Manager; access guarded by Manager.mu since all callers
// hold that mutex when reaching auto-relogin.
type reloginState struct {
	lastAttempt     time.Time
	consecutiveFail int
}

// isInvalidGrant matches the OAuth 2.0 error response that means the
// refresh token itself is dead and no amount of retrying with the
// same token will help. Anthropic returns this with HTTP 400 and a
// JSON body like {"error":"invalid_grant","error_description":"..."}.
func isInvalidGrant(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid_grant")
}

// autoRelogin runs `claude auth login` and returns nil on success.
// Caller must hold m.mu. Returns the original refresh error wrapped
// with a hint when the breaker is open or the rate limit fires; the
// caller should bubble that up unchanged.
func (m *Manager) autoRelogin(ctx context.Context, originalErr error) error {
	if !isInvalidGrant(originalErr) {
		return originalErr
	}

	if m.relogin.consecutiveFail >= reloginBreakerThreshold {
		slog.Warn("oauth.relogin.skipped",
			"subcomponent", "oauth",
			"reason", "breaker_open",
			"consecutive_fail", m.relogin.consecutiveFail,
		)
		return fmt.Errorf("%w; auto re-login disabled after %d failures, run `claude auth login` manually",
			originalErr, m.relogin.consecutiveFail)
	}

	if !m.relogin.lastAttempt.IsZero() && time.Since(m.relogin.lastAttempt) < reloginMinInterval {
		slog.Warn("oauth.relogin.skipped",
			"subcomponent", "oauth",
			"reason", "rate_limited",
			"since_last_ms", time.Since(m.relogin.lastAttempt).Milliseconds(),
		)
		return fmt.Errorf("%w; auto re-login rate-limited (one attempt per %s)",
			originalErr, reloginMinInterval)
	}

	// Cross-process lock: another clyde instance may also be relogging.
	// Wait briefly; whoever wins runs login, others re-read tokens.
	if m.credentialsDir == "" {
		return fmt.Errorf("relogin: empty credentials dir (original: %w)", originalErr)
	}
	if err := os.MkdirAll(m.credentialsDir, 0o700); err != nil {
		return fmt.Errorf("relogin mkdir: %w", err)
	}
	lockPath := filepath.Join(m.credentialsDir, ".clyde-relogin.lock")
	lock := flock.New(lockPath)
	lockCtx, cancel := context.WithTimeout(ctx, reloginTimeout)
	defer cancel()
	got, err := lock.TryLockContext(lockCtx, 500*time.Millisecond)
	if err != nil || !got {
		return fmt.Errorf("acquire relogin lock: %w (original: %v)", err, originalErr)
	}
	defer func() { _ = lock.Unlock() }()

	// Post-lock re-read: another process may have already relogged.
	if disk, rerr := readCredentials(m.credentialsDir, m.oauthCfg.KeychainService); rerr == nil && disk != nil && !isExpired(disk) {
		slog.Info("oauth.relogin.raced",
			"subcomponent", "oauth",
			"expires_at_ms", disk.ExpiresAt,
		)
		m.cached = disk
		return nil
	}

	m.relogin.lastAttempt = time.Now()
	started := time.Now()

	cmd := exec.CommandContext(lockCtx, "claude", "auth", "login")
	slog.Info("oauth.relogin.spawned",
		"subcomponent", "oauth",
		"binary", "claude",
		"args", []string{"auth", "login"},
		"timeout_s", int(reloginTimeout.Seconds()),
	)

	out, runErr := cmd.CombinedOutput()
	durationMs := time.Since(started).Milliseconds()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	if runErr != nil {
		m.relogin.consecutiveFail++
		slog.Error("oauth.relogin.failed",
			"subcomponent", "oauth",
			"exit_code", exitCode,
			"duration_ms", durationMs,
			"consecutive_fail", m.relogin.consecutiveFail,
			"output_tail", tailString(string(out), 400),
			slog.Any("err", runErr),
		)
		return fmt.Errorf("claude auth login failed (exit %d): %w (original: %v)",
			exitCode, runErr, originalErr)
	}

	m.relogin.consecutiveFail = 0
	slog.Info("oauth.relogin.completed",
		"subcomponent", "oauth",
		"exit_code", exitCode,
		"duration_ms", durationMs,
	)

	// Force a re-read on the next Token() call by dropping cache and
	// resetting mtime tracking. Caller should retry refresh once.
	m.cached = nil
	m.credsMtime = 0
	return nil
}

func tailString(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
