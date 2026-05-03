// Package oauth manages adapter OAuth token flows and persistence.
package oauth

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/providers/claude/oauthcredentials"
)

// Manager is goroutine-safe. One instance per daemon is enough.
type Manager struct {
	mu             sync.Mutex
	cached         *Tokens
	snapshot       credentialSnapshot
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
	log := oauthLog.Logger()
	m.mu.Lock()
	defer m.mu.Unlock()

	selected, err := m.selectedCredential(ctx)
	if err != nil {
		return "", err
	}
	tokens := selected.Tokens
	if !isExpired(tokens) {
		log.DebugContext(ctx, "oauth.auth.cache_hit",
			"subcomponent", "oauth",
			"store_kind", selected.Source,
			"expires_at_ms", tokens.ExpiresAt,
		)
		return tokens.AccessToken, nil
	}

	refreshStarted := oauthClock.Now()
	refreshable, refreshableErr := m.ensureRefreshableCandidate(ctx, selected)
	if refreshableErr != nil {
		log.ErrorContext(ctx, "oauth.store.unrefreshable",
			"subcomponent", "oauth",
			"duration_ms", time.Since(refreshStarted).Milliseconds(),
			"store_kind_summaries", summariesAsStrings(selected.Summaries),
			"err", refreshableErr,
		)
		return "", refreshableErr
	}
	refreshed, err := m.refreshLocked(ctx, refreshable)
	if err != nil {
		log.ErrorContext(ctx, "oauth.auth.refresh_failed",
			"subcomponent", "oauth",
			"duration_ms", time.Since(refreshStarted).Milliseconds(),
			"store_kind", refreshable.Source,
			"err", err,
		)
		if isInvalidGrant(err) {
			log.InfoContext(ctx, "oauth.refresh.invalid_grant_detected",
				"subcomponent", "oauth",
			)
			if reErr := m.autoRelogin(ctx, err); reErr != nil {
				return "", reErr
			}
			fresh, readErr := m.reselectCredential(ctx)
			if readErr != nil {
				log.WarnContext(ctx, "oauth.store.post_relogin_read_failed",
					"subcomponent", "oauth",
					"store_dir", m.credentialsDir,
					"keychain_service_present", m.oauthCfg.KeychainService != "",
					"err", readErr.Error(),
				)
				return "", fmt.Errorf("post-relogin read credentials: %w", readErr)
			}
			if !isExpired(fresh.Tokens) {
				log.InfoContext(ctx, "oauth.auth.refreshed_via_relogin",
					"subcomponent", "oauth",
					"duration_ms", time.Since(refreshStarted).Milliseconds(),
					"store_kind", fresh.Source,
					"expires_at_ms", fresh.Tokens.ExpiresAt,
				)
				return fresh.Tokens.AccessToken, nil
			}
			retryCredential, retrySelectErr := m.ensureRefreshableCandidate(ctx, fresh)
			if retrySelectErr != nil {
				return "", fmt.Errorf("post-relogin select refreshable credentials: %w", retrySelectErr)
			}
			retried, retryErr := m.refreshLocked(ctx, retryCredential)
			if retryErr != nil {
				log.ErrorContext(ctx, "oauth.auth.post_relogin_refresh_failed",
					"subcomponent", "oauth",
					"duration_ms", time.Since(refreshStarted).Milliseconds(),
					"store_kind", retryCredential.Source,
					"err", retryErr.Error(),
				)
				return "", fmt.Errorf("post-relogin refresh: %w", retryErr)
			}
			m.cacheTokens(oauthcredentials.SourceFile, retried)
			log.InfoContext(ctx, "oauth.auth.refreshed_via_relogin",
				"subcomponent", "oauth",
				"duration_ms", time.Since(refreshStarted).Milliseconds(),
				"expires_at_ms", retried.ExpiresAt,
			)
			return retried.AccessToken, nil
		}
		return "", err
	}
	m.cacheTokens(oauthcredentials.SourceFile, refreshed)
	log.InfoContext(ctx, "oauth.auth.refreshed",
		"subcomponent", "oauth",
		"duration_ms", time.Since(refreshStarted).Milliseconds(),
		"store_kind", refreshable.Source,
		"expires_at_ms", refreshed.ExpiresAt,
		"scopes", strings.Join(refreshed.Scopes, " "),
	)
	return refreshed.AccessToken, nil
}

func (m *Manager) selectedCredential(ctx context.Context) (*selectedCredential, error) {
	if m.cached == nil || m.cachedNeedsReselect() {
		return m.reselectCredential(ctx)
	}
	metadata := oauthcredentials.NewMetadata(m.cached, oauthClock.Now(), m.snapshot.FileMtime)
	return &selectedCredential{
		Source:   m.snapshot.Source,
		Tokens:   m.cached.Clone(),
		Metadata: metadata,
	}, nil
}

func (m *Manager) reselectCredential(ctx context.Context) (*selectedCredential, error) {
	results := readCredentialCandidates(ctx, m.credentialsDir, m.oauthCfg.KeychainService)
	selected, err := selectCredentialCandidate(results)
	if err != nil {
		return nil, err
	}
	m.cached = selected.Tokens.Clone()
	m.snapshot = snapshotForCredential(selected)
	oauthLog.Logger().InfoContext(ctx, "oauth.credentials.selected",
		"subcomponent", "oauth",
		"store_kind", selected.Source,
		"expires_at_ms", selected.Metadata.ExpiresAt,
		"refresh_token_present", selected.Metadata.RefreshTokenPresent,
		"store_kind_summaries", summariesAsStrings(selected.Summaries),
	)
	return selected, nil
}

func (m *Manager) cachedNeedsReselect() bool {
	if m.cached == nil {
		return true
	}
	if isExpired(m.cached) {
		return true
	}
	if m.cached.RefreshToken == "" {
		return true
	}
	if m.snapshot.Source == oauthcredentials.SourceFile {
		mtime := credentialsFileMtime(m.credentialsDir)
		return mtime != 0 && m.snapshot.FileMtime != 0 && mtime != m.snapshot.FileMtime
	}
	return false
}

func (m *Manager) ensureRefreshableCandidate(ctx context.Context, selected *selectedCredential) (*selectedCredential, error) {
	if selected != nil && selected.Tokens != nil && selected.Tokens.RefreshToken != "" {
		return selected, nil
	}
	results := readCredentialCandidates(ctx, m.credentialsDir, m.oauthCfg.KeychainService)
	refreshable, err := selectRefreshableCredential(results)
	if err != nil {
		return nil, err
	}
	m.cached = refreshable.Tokens.Clone()
	m.snapshot = snapshotForCredential(refreshable)
	oauthLog.Logger().InfoContext(ctx, "oauth.credentials.cache_invalidated",
		"subcomponent", "oauth",
		"reason", "cached_token_missing_refresh_token",
		"store_kind", refreshable.Source,
		"store_kind_summaries", summariesAsStrings(refreshable.Summaries),
	)
	return refreshable, nil
}

func (m *Manager) cacheTokens(source oauthcredentials.Source, tokens *Tokens) {
	m.cached = tokens.Clone()
	metadata := oauthcredentials.NewMetadata(tokens, oauthClock.Now(), credentialsFileMtime(m.credentialsDir))
	m.snapshot = credentialSnapshot{
		Source:              source,
		Fingerprint:         metadata.Fingerprint,
		ExpiresAt:           metadata.ExpiresAt,
		RefreshTokenPresent: metadata.RefreshTokenPresent,
		FileMtime:           metadata.FileMtime,
	}
}

func credentialsFileMtime(credentialsDir string) int64 {
	info, err := os.Stat(filepath.Join(credentialsDir, ".credentials.json"))
	if err != nil {
		return 0
	}
	return info.ModTime().UnixNano()
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
	return oauthClock.Now().Add(refreshSafetyWindow).After(expiresAt)
}
