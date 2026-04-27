// Package oauth manages adapter OAuth token flows and persistence.
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
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

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

	if disk, err := readCredentials(m.credentialsDir, m.oauthCfg.KeychainService); err == nil && disk != nil && !isExpired(disk) {
		slog.Info("oauth.token.refresh_raced",
			"subcomponent", "oauth",
			"expires_at_ms", disk.ExpiresAt,
		)
		return disk, nil
	}

	body, err := json.Marshal(map[string]any{
		"grant_type":    "refresh_token",
		"refresh_token": current.RefreshToken,
		"client_id":     m.oauthCfg.ClientID,
		"scope":         strings.Join(m.oauthCfg.Scopes, " "),
	})
	if err != nil {
		return nil, fmt.Errorf("marshal refresh body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.oauthCfg.TokenURL, bytes.NewReader(body))
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
			"subcomponent", "oauth",
			"err", err,
		)
	}
	return newTokens, nil
}
