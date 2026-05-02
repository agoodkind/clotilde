// Package oauth manages adapter OAuth token flows and persistence.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goodkind.io/clyde/internal/config"
)

// Manager is goroutine-safe. One instance per daemon is enough.
type Manager struct {
	mu             sync.Mutex
	cached         *Tokens
	credsMtime     int64
	httpClient     *http.Client
	credentialsDir string
	oauthCfg       config.AdapterOAuth
	relogin        reloginState
}

// NewManager builds a Manager. oauthCfg supplies token URL, client id,
// scopes, and keychain service name. credentialsDir defaults to
// $HOME/.claude when empty; the lock file lives directly in that
// directory.
func NewManager(oauthCfg config.AdapterOAuth, credentialsDir string) *Manager {
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
		oauthCfg:       oauthCfg,
	}
}

// Token returns a non-expired access token, refreshing if needed.
// Concurrent callers share one in-flight refresh thanks to the
// per-process mutex; cross-process races are handled by the file
// lock and post-lock re-read.
func (m *Manager) Token(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.invalidateIfDiskChanged()

	tokens := m.cached
	if tokens == nil {
		fresh, err := readCredentials(m.credentialsDir, m.oauthCfg.KeychainService)
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
		oauthLog.Logger().Debug("oauth.token.cache_hit",
			"subcomponent", "oauth",
			"expires_at_ms", tokens.ExpiresAt,
		)
		return tokens.AccessToken, nil
	}

	refreshStarted := time.Now()
	refreshed, err := m.refreshLocked(ctx, tokens)
	if err != nil {
		oauthLog.Logger().Error("oauth.token.refresh_failed",
			"subcomponent", "oauth",
			"duration_ms", time.Since(refreshStarted).Milliseconds(),
			"err", err,
		)
		if isInvalidGrant(err) {
			oauthLog.Logger().Info("oauth.refresh.invalid_grant_detected",
				"subcomponent", "oauth",
			)
			if reErr := m.autoRelogin(ctx, err); reErr != nil {
				return "", reErr
			}
			fresh, readErr := readCredentials(m.credentialsDir, m.oauthCfg.KeychainService)
			if readErr != nil {
				return "", fmt.Errorf("post-relogin read credentials: %w", readErr)
			}
			if fresh == nil {
				return "", errors.New("post-relogin: no tokens found in credentials store")
			}
			m.cached = fresh
			if !isExpired(fresh) {
				oauthLog.Logger().Info("oauth.token.refreshed_via_relogin",
					"subcomponent", "oauth",
					"duration_ms", time.Since(refreshStarted).Milliseconds(),
					"expires_at_ms", fresh.ExpiresAt,
				)
				return fresh.AccessToken, nil
			}
			retried, retryErr := m.refreshLocked(ctx, fresh)
			if retryErr != nil {
				return "", fmt.Errorf("post-relogin refresh: %w", retryErr)
			}
			m.cached = retried
			oauthLog.Logger().Info("oauth.token.refreshed_via_relogin",
				"subcomponent", "oauth",
				"duration_ms", time.Since(refreshStarted).Milliseconds(),
				"expires_at_ms", retried.ExpiresAt,
			)
			return retried.AccessToken, nil
		}
		return "", err
	}
	m.cached = refreshed
	oauthLog.Logger().Info("oauth.token.refreshed",
		"subcomponent", "oauth",
		"duration_ms", time.Since(refreshStarted).Milliseconds(),
		"expires_at_ms", refreshed.ExpiresAt,
		"scopes", strings.Join(refreshed.Scopes, " "),
	)
	return refreshed.AccessToken, nil
}

// invalidateIfDiskChanged drops the in-memory cache if the
// .credentials.json mtime moved (i.e. another process wrote new
// tokens). Caller must hold m.mu.
func (m *Manager) invalidateIfDiskChanged() {
	credsPath := filepath.Join(m.credentialsDir, ".credentials.json")
	info, err := os.Stat(credsPath)
	if err != nil {
		// File-less keychain path on macOS, or first run. Either way
		// don't invalidate.
		return
	}
	mtime := info.ModTime().UnixNano()
	if mtime != m.credsMtime {
		if m.credsMtime != 0 {
			oauthLog.Logger().Info("oauth.credentials.disk_changed",
				"subcomponent", "oauth",
				"path", credsPath,
			)
		}
		m.credsMtime = mtime
		m.cached = nil
	}
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
