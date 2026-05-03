// Package oauth manages adapter OAuth token flows and persistence.
package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

type oauthRefreshRequest struct {
	GrantType    string `json:"grant_type"`
	RefreshToken string `json:"refresh_token"`
	ClientID     string `json:"client_id"`
	Scope        string `json:"scope"`
}

type oauthRefreshResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

// refreshLocked performs the disk lock dance, double-checks for a
// concurrent refresh by another process, then calls the token
// endpoint and persists the new tokens. Caller must hold m.mu.
func (m *Manager) refreshLocked(ctx context.Context, current *selectedCredential) (*Tokens, error) {
	log := oauthLog.Logger()
	if current == nil || current.Tokens == nil || current.Tokens.RefreshToken == "" {
		return nil, errors.New("oauth token expired and no refresh_token available; re-run `claude /login`")
	}

	if err := os.MkdirAll(m.credentialsDir, 0o700); err != nil {
		log.WarnContext(ctx, "oauth.store.mkdir_failed",
			"subcomponent", "oauth",
			"store_dir", m.credentialsDir,
			"err", err.Error(),
		)
		return nil, fmt.Errorf("mkdir credentials dir: %w", err)
	}
	lockPath := filepath.Join(m.credentialsDir, ".clyde-oauth.lock")
	lock := flock.New(lockPath)

	lockCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	got, err := lock.TryLockContext(lockCtx, 200*time.Millisecond)
	if err != nil {
		log.WarnContext(ctx, "oauth.lock.acquire_failed",
			"subcomponent", "oauth",
			"lock_path", lockPath,
			"err", err.Error(),
		)
		return nil, fmt.Errorf("acquire oauth lock: %w", err)
	}
	if !got {
		log.WarnContext(ctx, "oauth.lock.acquire_timeout",
			"subcomponent", "oauth",
			"lock_path", lockPath,
		)
		return nil, errors.New("acquire oauth lock: timed out")
	}
	defer func() { _ = lock.Unlock() }()

	if raced, racedErr := m.reselectCredential(ctx); racedErr == nil && raced != nil && !isExpired(raced.Tokens) {
		oauthLog.Logger().InfoContext(ctx, "oauth.token.refresh_raced",
			"subcomponent", "oauth",
			"credential_source", raced.Source,
			"expires_at_ms", raced.Tokens.ExpiresAt,
		)
		return raced.Tokens.Clone(), nil
	}

	requestPayload := oauthRefreshRequest{
		GrantType:    "refresh_token",
		RefreshToken: current.Tokens.RefreshToken,
		ClientID:     m.oauthCfg.ClientID,
		Scope:        strings.Join(m.oauthCfg.Scopes, " "),
	}
	body, err := json.Marshal(requestPayload)
	if err != nil {
		log.WarnContext(ctx, "oauth.refresh.body_marshal_failed",
			"subcomponent", "oauth",
			"scope_count", len(m.oauthCfg.Scopes),
			"client_id_present", m.oauthCfg.ClientID != "",
			"err", err.Error(),
		)
		return nil, fmt.Errorf("marshal refresh body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.oauthCfg.TokenURL, bytes.NewReader(body))
	if err != nil {
		log.WarnContext(ctx, "oauth.refresh.request_build_failed",
			"subcomponent", "oauth",
			"endpoint_url", m.oauthCfg.TokenURL,
			"body_bytes", len(body),
			"err", err.Error(),
		)
		return nil, fmt.Errorf("build refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := m.httpClient.Do(req)
	if err != nil {
		log.WarnContext(ctx, "oauth.refresh.post_failed",
			"subcomponent", "oauth",
			"endpoint_url", m.oauthCfg.TokenURL,
			"err", err.Error(),
		)
		return nil, fmt.Errorf("post refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh failed: %s: %s", resp.Status, truncate(string(respBytes), 400))
	}

	var refreshResp oauthRefreshResponse
	if err := json.Unmarshal(respBytes, &refreshResp); err != nil {
		log.WarnContext(ctx, "oauth.refresh.response_decode_failed",
			"subcomponent", "oauth",
			"status", resp.StatusCode,
			"body_bytes", len(respBytes),
			"err", err.Error(),
		)
		return nil, fmt.Errorf("decode refresh response: %w", err)
	}
	if refreshResp.AccessToken == "" {
		return nil, fmt.Errorf("refresh response missing access_token: %s", truncate(string(respBytes), 400))
	}

	newTokens := &Tokens{
		AccessToken:      refreshResp.AccessToken,
		RefreshToken:     coalesce(refreshResp.RefreshToken, current.Tokens.RefreshToken),
		ExpiresAt:        oauthClock.Now().UnixMilli() + refreshResp.ExpiresIn*1000,
		Scopes:           splitScopes(refreshResp.Scope, current.Tokens.Scopes),
		SubscriptionType: current.Tokens.SubscriptionType,
		RateLimitTier:    current.Tokens.RateLimitTier,
	}

	if err := writeCredentials(ctx, m.credentialsDir, newTokens); err != nil {
		oauthLog.Logger().WarnContext(ctx, "oauth.credentials.write_failed",
			"subcomponent", "oauth",
			"err", err,
		)
	}
	return newTokens, nil
}
