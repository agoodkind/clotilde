// Public Manager API: token cache and coordination.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Manager is goroutine-safe. One instance per daemon is enough.
type Manager struct {
	mu             sync.Mutex
	cached         *Tokens
	credsMtime     int64
	httpClient     *http.Client
	credentialsDir string
}

// NewManager builds a Manager. credentialsDir defaults to
// $HOME/.claude when empty; the lock file lives directly in that
// directory.
func NewManager(credentialsDir string) *Manager {
	if credentialsDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			credentialsDir = filepath.Join(home, ".claude")
		}
	}
	return &Manager{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		credentialsDir: credentialsDir,
	}
}

// Token returns a non-expired access token, refreshing if needed.
// Concurrent callers share one in-flight refresh thanks to the
// per-process mutex; cross-process races are handled by the file
// lock and post-lock re-read.
func (m *Manager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.invalidateIfDiskChanged(); err != nil {
		// Non fatal; the cached value is still usable if it parses.
	}

	tokens := m.cached
	if tokens == nil {
		fresh, err := readCredentials(m.credentialsDir)
		if err != nil {
			return "", fmt.Errorf("read credentials: %w", err)
		}
		if fresh == nil {
			return "", errors.New("no claudeAiOauth tokens found; run `claude /login` first")
		}
		tokens = fresh
		m.cached = fresh
	}

	if !isExpired(tokens) {
		slog.Debug("oauth.token.cache_hit",
			"component", "oauth",
			"expires_at_ms", tokens.ExpiresAt,
		)
		return tokens.AccessToken, nil
	}

	refreshStarted := time.Now()
	refreshed, err := m.refreshLocked(ctx, tokens)
	if err != nil {
		slog.Error("oauth.token.refresh_failed",
			"component", "oauth",
			"duration_ms", time.Since(refreshStarted).Milliseconds(),
			slog.Any("err", err),
		)
		return "", err
	}
	m.cached = refreshed
	slog.Info("oauth.token.refreshed",
		"component", "oauth",
		"duration_ms", time.Since(refreshStarted).Milliseconds(),
		"expires_at_ms", refreshed.ExpiresAt,
		"scopes", strings.Join(refreshed.Scopes, " "),
	)
	return refreshed.AccessToken, nil
}

// invalidateIfDiskChanged drops the in-memory cache if the
// .credentials.json mtime moved (i.e. another process wrote new
// tokens). Caller must hold m.mu.
func (m *Manager) invalidateIfDiskChanged() error {
	credsPath := filepath.Join(m.credentialsDir, ".credentials.json")
	info, err := os.Stat(credsPath)
	if err != nil {
		// File-less keychain path on macOS, or first run. Either way
		// don't invalidate.
		return nil
	}
	mtime := info.ModTime().UnixNano()
	if mtime != m.credsMtime {
		if m.credsMtime != 0 {
			slog.Info("oauth.credentials.disk_changed",
				"component", "oauth",
				"path", credsPath,
			)
		}
		m.credsMtime = mtime
		m.cached = nil
	}
	return nil
}

// isExpired returns true when the token is past its expiry minus the
// safety window. Tokens with ExpiresAt == 0 (env-var inference-only
// tokens that the CLI synthesizes) are treated as never-expiring.
func isExpired(t *Tokens) bool {
	if t == nil || t.AccessToken == "" {
		return true
	}
	if t.ExpiresAt == 0 {
		return false
	}
	expiresAt := time.UnixMilli(t.ExpiresAt)
	return time.Now().Add(refreshSafetyWindow).After(expiresAt)
}
