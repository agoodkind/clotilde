package anthropic

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Identity carries the three IDs claude-cli serializes into
// metadata.user_id. The wire form is a JSON-encoded string of
// MetadataUserID.
type Identity struct {
	DeviceID    string
	AccountUUID string
	SessionID   string
}

// EncodeUserID returns the JSON string claude-cli puts at
// metadata.user_id.
func (i Identity) EncodeUserID() string {
	if i.DeviceID == "" && i.AccountUUID == "" && i.SessionID == "" {
		return ""
	}
	encoded, err := json.Marshal(MetadataUserID(i))
	if err != nil {
		return ""
	}
	return string(encoded)
}

var (
	deviceOnce sync.Once
	deviceID   string
	deviceErr  error
)

// DeviceID returns a stable per-machine identifier persisted under
// XDG_STATE_HOME (default ~/.local/state). The value is a sha256 hex
// digest of a one-time-generated UUID, matching claude-cli's hex
// device_id shape (64 hex chars).
func DeviceID() (string, error) {
	deviceOnce.Do(func() {
		deviceID, deviceErr = readOrGenerateDeviceID()
	})
	return deviceID, deviceErr
}

func readOrGenerateDeviceID() (string, error) {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home dir: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(stateHome, "clyde", "adapter", "anthropic")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create device dir: %w", err)
	}
	path := filepath.Join(dir, "device_id")
	if data, err := os.ReadFile(path); err == nil {
		if v := strings.TrimSpace(string(data)); v != "" {
			return v, nil
		}
	}
	seed := uuid.NewString()
	sum := sha256.Sum256([]byte(seed))
	id := hex.EncodeToString(sum[:])
	if err := os.WriteFile(path, []byte(id), 0o600); err != nil {
		return "", fmt.Errorf("persist device_id: %w", err)
	}
	return id, nil
}

// AccountUUIDFromAccessToken extracts the `sub` claim from a JWT
// access token. Anthropic's OAuth tokens are opaque (`sk-ant-oat...`),
// not JWTs, so this is a fallback that returns empty for the
// real-world case. Kept for parity with other providers that may
// issue JWT access tokens.
func AccountUUIDFromAccessToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some IdPs emit standard padded base64.
		payload, err = base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			return ""
		}
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.Sub
}

// AccountUUIDFromClaudeConfig reads ~/.claude.json (claude-cli's
// state file) and returns oauthAccount.accountUuid. This mirrors
// where claude-cli reads its own account_uuid from for the
// metadata.user_id payload. Returns empty string and a non-nil
// error when the file is missing, unreadable, or has no
// oauthAccount entry.
func AccountUUIDFromClaudeConfig() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	path := filepath.Join(home, ".claude.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var doc struct {
		OAuthAccount struct {
			AccountUUID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", err
	}
	return strings.TrimSpace(doc.OAuthAccount.AccountUUID), nil
}
