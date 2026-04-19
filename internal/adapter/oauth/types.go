// Credential loading, refresh, and token types.
package oauth

import (
	"encoding/json"
	"time"
)

// refreshSafetyWindow is how far before expiresAt we proactively refresh.
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
