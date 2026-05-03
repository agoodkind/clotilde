package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/providers/claude/oauthcredentials"
)

func TestSelectCredentialCandidate_KeychainWinsWhenEquallyUsable(t *testing.T) {
	now := oauthClock.Now()
	keychain := credentialReadResult(oauthcredentials.SourceKeychain, tokenWithExpiry(now.Add(time.Hour), true), now)
	file := credentialReadResult(oauthcredentials.SourceFile, tokenWithExpiry(now.Add(time.Hour), true), now)
	selected, err := selectCredentialCandidate([]oauthcredentials.ReadResult{file, keychain})
	if err != nil {
		t.Fatalf("selectCredentialCandidate error = %v", err)
	}
	if selected.Source != oauthcredentials.SourceKeychain {
		t.Fatalf("source = %q, want keychain", selected.Source)
	}
}

func TestSelectCredentialCandidate_FileWinsWhenKeychainExpiredWithoutRefresh(t *testing.T) {
	now := oauthClock.Now()
	keychain := credentialReadResult(oauthcredentials.SourceKeychain, tokenWithExpiry(now.Add(-time.Hour), false), now)
	file := credentialReadResult(oauthcredentials.SourceFile, tokenWithExpiry(now.Add(time.Hour), true), now)
	selected, err := selectCredentialCandidate([]oauthcredentials.ReadResult{keychain, file})
	if err != nil {
		t.Fatalf("selectCredentialCandidate error = %v", err)
	}
	if selected.Source != oauthcredentials.SourceFile {
		t.Fatalf("source = %q, want credentials_file", selected.Source)
	}
}

func TestSelectCredentialCandidate_SummaryDoesNotLeakSecrets(t *testing.T) {
	now := oauthClock.Now()
	result := credentialReadResult(oauthcredentials.SourceFile, &Tokens{
		AccessToken:  "access-super-secret",
		RefreshToken: "refresh-super-secret",
		ExpiresAt:    now.Add(time.Hour).UnixMilli(),
	}, now)
	selected, err := selectCredentialCandidate([]oauthcredentials.ReadResult{result})
	if err != nil {
		t.Fatalf("selectCredentialCandidate error = %v", err)
	}
	encoded, err := json.Marshal(selected.Summaries)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	encodedText := string(encoded)
	if strings.Contains(encodedText, "access-super-secret") || strings.Contains(encodedText, "refresh-super-secret") {
		t.Fatalf("summary leaked token value: %s", encodedText)
	}
}

func TestToken_RereadsWhenCachedTokenCannotRefresh(t *testing.T) {
	dir := t.TempDir()
	now := oauthClock.Now()
	writeOAuthCredentialFile(t, dir, tokenWithExpiry(now.Add(time.Hour), true))
	manager := NewManager(config.AdapterOAuth{}, dir)
	manager.cached = tokenWithExpiry(now.Add(-time.Hour), false)
	manager.snapshot = credentialSnapshot{
		Source:              oauthcredentials.SourceKeychain,
		RefreshTokenPresent: false,
	}
	token, err := manager.Token(context.Background())
	if err != nil {
		t.Fatalf("Token error = %v", err)
	}
	if token != "access-token" {
		t.Fatalf("token = %q, want access-token", token)
	}
	if manager.snapshot.Source != oauthcredentials.SourceFile {
		t.Fatalf("cached source = %q, want credentials_file", manager.snapshot.Source)
	}
}

func TestRefreshLocked_UsesTypedPayloadAndWritesFile(t *testing.T) {
	dir := t.TempDir()
	var requestBody oauthRefreshRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"refreshed-access","refresh_token":"refreshed-refresh","expires_in":3600,"scope":"user:profile user:inference"}`))
	}))
	defer server.Close()
	manager := NewManager(config.AdapterOAuth{
		TokenURL: server.URL,
		ClientID: "client-id",
		Scopes:   []string{"user:profile", "user:inference"},
	}, dir)
	selected := &selectedCredential{
		Source: oauthcredentials.SourceFile,
		Tokens: &Tokens{
			AccessToken:  "expired-access",
			RefreshToken: "refresh-input",
			ExpiresAt:    oauthClock.Now().Add(-time.Hour).UnixMilli(),
		},
	}
	refreshed, err := manager.refreshLocked(context.Background(), selected)
	if err != nil {
		t.Fatalf("refreshLocked error = %v", err)
	}
	if refreshed.AccessToken != "refreshed-access" {
		t.Fatalf("refreshed access token = %q, want refreshed-access", refreshed.AccessToken)
	}
	if requestBody.GrantType != "refresh_token" || requestBody.RefreshToken != "refresh-input" || requestBody.ClientID != "client-id" {
		t.Fatalf("request body = %+v, want typed refresh payload", requestBody)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".credentials.json"))
	if err != nil {
		t.Fatalf("read written credentials: %v", err)
	}
	if !strings.Contains(string(data), "refreshed-access") {
		t.Fatalf("written credentials did not include refreshed access token: %s", string(data))
	}
}

func credentialReadResult(source oauthcredentials.Source, tokens *Tokens, now time.Time) oauthcredentials.ReadResult {
	metadata := oauthcredentials.NewMetadata(tokens, now, 0)
	return oauthcredentials.ReadResult{
		Source:   source,
		Tokens:   tokens,
		Present:  tokens != nil,
		Metadata: metadata,
	}
}

func tokenWithExpiry(expiresAt time.Time, refreshTokenPresent bool) *Tokens {
	refreshToken := ""
	if refreshTokenPresent {
		refreshToken = "refresh-token"
	}
	return &Tokens{
		AccessToken:  "access-token",
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt.UnixMilli(),
	}
}

func writeOAuthCredentialFile(t *testing.T, dir string, tokens *Tokens) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		ClaudeAIOauth *Tokens `json:"claudeAiOauth"`
	}{ClaudeAIOauth: tokens})
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), encoded, 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
}
