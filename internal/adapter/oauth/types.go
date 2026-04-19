// Constants and credential document types.
package oauth

import (
	"encoding/json"
	"time"
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
