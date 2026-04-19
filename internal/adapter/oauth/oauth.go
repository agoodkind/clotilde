// Package oauth reads, caches, and refreshes the Claude Code OAuth
// tokens that the official `claude` CLI stores in the macOS keychain
// (or in ~/.claude/.credentials.json as a fallback).
//
// The adapter uses these tokens to call Anthropic's /v1/messages API
// directly with Bearer auth, so requests bill against the user's
// Claude.ai subscription bucket (Pro/Max/Team/Enterprise) instead of
// the metered API. This mirrors how the `claude` CLI itself
// authenticates when isClaudeAISubscriber() is true.
//
// Behavior verified against the claude-code 2.1.88 sourcemap:
//   - tokens live under keychain service "REDACTED-KEYCHAIN"
//     and a file at $CLAUDE_CONFIG_DIR/.credentials.json
//   - the JSON document has a top level "claudeAiOauth" key with
//     accessToken, refreshToken, expiresAt (ms), scopes, etc.
//   - refresh is POST https://REDACTED-OAUTH-HOST/v1/oauth/token
//     with body { grant_type, refresh_token, client_id, scope }
//   - the public CLIENT_ID is REDACTED-CLIENT-ID
//   - inference calls send Authorization: Bearer + anthropic-beta:
//     REDACTED-OAUTH-BETA + anthropic-version: 2023-06-01
//
// Cross process safety mirrors claude's own implementation: a file
// lock under $CLAUDE_CONFIG_DIR coordinates refresh and the on disk
// credentials file's mtime is sampled to detect refreshes performed
// by other processes (another `claude` instance or a re-login).
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/flock"
)

// TokenURL is the Claude.ai OAuth token endpoint used for refresh.
const TokenURL = "https://REDACTED-OAUTH-HOST/v1/oauth/token"

// ClientID is the Claude Code public OAuth client. Hardcoded in the
// CLI at src/constants/oauth.ts:99.
const ClientID = "REDACTED-CLIENT-ID"

// BetaHeader is the Anthropic-Beta value Claude Code sends with
// every OAuth-authenticated /v1/messages call.
const BetaHeader = "REDACTED-OAUTH-BETA"

// Version is the Anthropic-Version value paired with BetaHeader.
const Version = "2023-06-01"

// DefaultScopes is the full Claude.ai subscription scope set.
// Refresh requests can ask for these regardless of what the original
// authorize granted; the backend allows scope expansion on refresh.
var DefaultScopes = []string{
	"user:profile",
	"user:inference",
	"user:sessions:claude_code",
	"user:mcp_servers",
	"user:file_upload",
}

// keychainService is the macOS keychain entry the CLI writes to.
const keychainService = "REDACTED-KEYCHAIN"

// refreshSafetyWindow is how far before expiresAt we proactively
// refresh. Mirrors the CLI's behavior of refreshing when a token is
// "expired" inclusive of clock skew.
const refreshSafetyWindow = 30 * time.Second

// Tokens is the credential document layout. Field tags use the
// camelCase keys the CLI persists; we accept extra fields silently.
type Tokens struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

// credentialsDoc is the wrapper the CLI stores in keychain or
// .credentials.json. Other top level keys (mcpOAuth,
// organizationUuid, ...) are tolerated.
type credentialsDoc struct {
	ClaudeAIOauth *Tokens `json:"claudeAiOauth,omitempty"`
	// Catch all so we don't drop fields when writing back.
	Raw map[string]json.RawMessage `json:"-"`
}

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

// refreshLocked performs the disk lock dance, double-checks for a
// concurrent refresh by another process, then calls the token
// endpoint and persists the new tokens. Caller must hold m.mu.
func (m *Manager) refreshLocked(ctx context.Context, current *Tokens) (*Tokens, error) {
	if current.RefreshToken == "" {
		return nil, errors.New("oauth token expired and no refresh_token available; re-run `claude /login`")
	}

	if err := os.MkdirAll(m.credentialsDir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir credentials dir: %w", err)
	}
	lockPath := filepath.Join(m.credentialsDir, ".clyde-oauth.lock")
	lock := flock.New(lockPath)

	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	got, err := lock.TryLockContext(lockCtx, 200*time.Millisecond)
	if err != nil || !got {
		return nil, fmt.Errorf("acquire oauth lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	if disk, err := readCredentials(m.credentialsDir); err == nil && disk != nil && !isExpired(disk) {
		slog.Info("oauth.token.refresh_raced",
			"component", "oauth",
			"expires_at_ms", disk.ExpiresAt,
		)
		return disk, nil
	}

	body, err := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": current.RefreshToken,
		"client_id":     ClientID,
		"scope":         strings.Join(DefaultScopes, " "),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal refresh body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("post refresh: %w", err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: %s: %s", resp.Status, truncate(string(respBytes), 400))
	}

	var refreshResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		Scope        string `json:"scope"`
	}
	if err := json.Unmarshal(respBytes, &refreshResp); err != nil {
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if refreshResp.AccessToken == "" {
		return nil, fmt.Errorf("refresh response missing access_token: %s", truncate(string(respBytes), 400))
	}

	newTokens := &Tokens{
		AccessToken:      refreshResp.AccessToken,
		RefreshToken:     coalesce(refreshResp.RefreshToken, current.RefreshToken),
		ExpiresAt:        time.Now().UnixMilli() + refreshResp.ExpiresIn*1000,
		Scopes:           splitScopes(refreshResp.Scope, current.Scopes),
		SubscriptionType: current.SubscriptionType,
		RateLimitTier:    current.RateLimitTier,
	}

	if err := writeCredentials(m.credentialsDir, newTokens); err != nil {
		slog.Warn("oauth.credentials.write_failed",
			"component", "oauth",
			slog.Any("err", err),
		)
	}
	return newTokens, nil
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

// readCredentials returns the parsed Tokens from whichever store is
// available. macOS prefers Keychain; on read failure we fall through
// to ~/.claude/.credentials.json. Returns nil tokens when neither
// store has a claudeAiOauth entry.
func readCredentials(dir string) (*Tokens, error) {
	if runtime.GOOS == "darwin" {
		if t, err := readKeychain(); err == nil && t != nil {
			return t, nil
		}
	}
	return readCredentialsFile(filepath.Join(dir, ".credentials.json"))
}

func readKeychain() (*Tokens, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-w")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("security find-generic-password: %w", err)
	}
	out = bytes.TrimSpace(out)
	return parseCredentialsBlob(out)
}

func readCredentialsFile(path string) (*Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseCredentialsBlob(data)
}

func parseCredentialsBlob(data []byte) (*Tokens, error) {
	var doc credentialsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	return doc.ClaudeAIOauth, nil
}

// writeCredentials updates the on-disk credentials file with new
// claudeAiOauth tokens, preserving any other top-level keys (mcpOAuth
// etc) that the CLI might have written. We do not write to keychain
// directly; the next `claude` CLI invocation will detect the file
// mtime change and resync (per the CLI's own
// invalidateOAuthCacheIfDiskChanged logic).
func writeCredentials(dir string, tokens *Tokens) error {
	credsPath := filepath.Join(dir, ".credentials.json")

	merged := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(credsPath); err == nil {
		_ = json.Unmarshal(existing, &merged)
	}

	encoded, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}
	merged["claudeAiOauth"] = encoded

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged credentials: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir credentials dir: %w", err)
	}
	tmp := credsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write temp credentials: %w", err)
	}
	if err := os.Rename(tmp, credsPath); err != nil {
		return fmt.Errorf("rename temp credentials: %w", err)
	}
	return nil
}

func coalesce(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitScopes(raw string, fallback []string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	return strings.Fields(raw)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
