// Package claude contains Claude Code integration helpers.
package claude

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// OAuthCredentialSource identifies a local Claude Code credential store.
type OAuthCredentialSource string

const (
	OAuthCredentialSourceKeychain OAuthCredentialSource = "keychain"
	OAuthCredentialSourceFile     OAuthCredentialSource = "credentials_file"
)

// OAuthTokens is the claudeAiOauth credential payload written by Claude Code.
type OAuthTokens struct {
	AccessToken      string   `json:"accessToken"`
	RefreshToken     string   `json:"refreshToken"`
	ExpiresAt        int64    `json:"expiresAt"`
	Scopes           []string `json:"scopes,omitempty"`
	SubscriptionType string   `json:"subscriptionType,omitempty"`
	RateLimitTier    string   `json:"rateLimitTier,omitempty"`
}

// Clone returns a deep copy so callers can cache tokens without sharing slices.
func (t *OAuthTokens) Clone() *OAuthTokens {
	if t == nil {
		return nil
	}
	clone := *t
	clone.Scopes = append([]string(nil), t.Scopes...)
	return &clone
}

// OAuthCredentialMetadata is safe to log. It never contains token values.
type OAuthCredentialMetadata struct {
	AccessTokenPresent  bool     `json:"accessTokenPresent"`
	RefreshTokenPresent bool     `json:"refreshTokenPresent"`
	ExpiresAtPresent    bool     `json:"expiresAtPresent"`
	ExpiresAt           int64    `json:"expiresAt"`
	Expired             bool     `json:"expired"`
	Scopes              []string `json:"scopes,omitempty"`
	Fingerprint         string   `json:"fingerprint,omitempty"`
	FileMtime           int64    `json:"fileMtime,omitempty"`
}

// OAuthCredentialReadResult describes one credential source read.
type OAuthCredentialReadResult struct {
	Source   OAuthCredentialSource   `json:"source"`
	Tokens   *OAuthTokens            `json:"-"`
	Present  bool                    `json:"present"`
	Err      error                   `json:"-"`
	Metadata OAuthCredentialMetadata `json:"metadata"`
}

// OAuthCredentialWriteResult describes one credential source write.
type OAuthCredentialWriteResult struct {
	Source OAuthCredentialSource `json:"source"`
	Err    error                 `json:"-"`
}

// OAuthCredentialSummary is safe to include in structured logs and errors.
type OAuthCredentialSummary struct {
	Source              OAuthCredentialSource `json:"source"`
	Present             bool                  `json:"present"`
	AccessTokenPresent  bool                  `json:"accessTokenPresent"`
	RefreshTokenPresent bool                  `json:"refreshTokenPresent"`
	ExpiresAtPresent    bool                  `json:"expiresAtPresent"`
	ExpiresAt           int64                 `json:"expiresAt"`
	Expired             bool                  `json:"expired"`
	Fingerprint         string                `json:"fingerprint,omitempty"`
	FileMtime           int64                 `json:"fileMtime,omitempty"`
	ParseError          string                `json:"parseError,omitempty"`
}

// OAuthCredentialReadOptions configures Claude Code credential discovery.
type OAuthCredentialReadOptions struct {
	CredentialsDir  string
	KeychainService string
	SecurityBinary  string
	Now             time.Time
}

// OAuthCredentialStore reads and optionally writes one Claude OAuth store.
type OAuthCredentialStore interface {
	Source() OAuthCredentialSource
	Read(ctx context.Context) OAuthCredentialReadResult
	Write(ctx context.Context, tokens *OAuthTokens) OAuthCredentialWriteResult
}

type oauthCredentialsDocument struct {
	ClaudeAIOauth *OAuthTokens `json:"claudeAiOauth,omitempty"`
}

// ReadOAuthCredentialCandidates reads every configured Claude OAuth credential source.
func ReadOAuthCredentialCandidates(ctx context.Context, options OAuthCredentialReadOptions) []OAuthCredentialReadResult {
	options = normalizeOAuthCredentialReadOptions(options)
	stores := oauthCredentialStores(options)
	results := make([]OAuthCredentialReadResult, 0, len(stores))
	for _, store := range stores {
		select {
		case <-ctx.Done():
			results = append(results, OAuthCredentialReadResult{
				Source: store.Source(),
				Err:    ctx.Err(),
			})
			continue
		default:
		}
		results = append(results, store.Read(ctx))
	}
	return results
}

// WriteOAuthCredentialsFile writes claudeAiOauth tokens to ~/.claude/.credentials.json.
func WriteOAuthCredentialsFile(ctx context.Context, credentialsDir string, tokens *OAuthTokens) error {
	options := normalizeOAuthCredentialReadOptions(OAuthCredentialReadOptions{CredentialsDir: credentialsDir})
	store := fileOAuthCredentialStore{credentialsDir: options.CredentialsDir, now: options.Now}
	result := store.Write(ctx, tokens)
	return result.Err
}

// SummarizeOAuthCredentialResults returns log-safe summaries for credential reads.
func SummarizeOAuthCredentialResults(results []OAuthCredentialReadResult) []OAuthCredentialSummary {
	summaries := make([]OAuthCredentialSummary, 0, len(results))
	for _, result := range results {
		summary := OAuthCredentialSummary{
			Source:              result.Source,
			Present:             result.Present,
			AccessTokenPresent:  result.Metadata.AccessTokenPresent,
			RefreshTokenPresent: result.Metadata.RefreshTokenPresent,
			ExpiresAtPresent:    result.Metadata.ExpiresAtPresent,
			ExpiresAt:           result.Metadata.ExpiresAt,
			Expired:             result.Metadata.Expired,
			Fingerprint:         result.Metadata.Fingerprint,
			FileMtime:           result.Metadata.FileMtime,
		}
		if result.Err != nil {
			summary.ParseError = result.Err.Error()
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

// NewOAuthCredentialMetadata builds safe metadata for a token payload.
func NewOAuthCredentialMetadata(tokens *OAuthTokens, now time.Time, fileMtime int64) OAuthCredentialMetadata {
	if now.IsZero() {
		now = time.Now()
	}
	metadata := OAuthCredentialMetadata{FileMtime: fileMtime}
	if tokens == nil {
		return metadata
	}
	metadata.AccessTokenPresent = tokens.AccessToken != ""
	metadata.RefreshTokenPresent = tokens.RefreshToken != ""
	metadata.ExpiresAtPresent = tokens.ExpiresAt != 0
	metadata.ExpiresAt = tokens.ExpiresAt
	metadata.Expired = oauthTokensExpiredAt(tokens, now)
	metadata.Scopes = append([]string(nil), tokens.Scopes...)
	metadata.Fingerprint = OAuthCredentialFingerprint(tokens)
	return metadata
}

// OAuthCredentialFingerprint returns a stable, non-secret fingerprint for diagnostics.
func OAuthCredentialFingerprint(tokens *OAuthTokens) string {
	if tokens == nil {
		return ""
	}
	scopes := append([]string(nil), tokens.Scopes...)
	sort.Strings(scopes)
	hash := sha256.New()
	writeHashPart(hash, tokens.AccessToken)
	writeHashPart(hash, tokens.RefreshToken)
	writeHashPart(hash, strconv.FormatInt(tokens.ExpiresAt, 10))
	writeHashPart(hash, strings.Join(scopes, " "))
	writeHashPart(hash, tokens.SubscriptionType)
	writeHashPart(hash, tokens.RateLimitTier)
	return hex.EncodeToString(hash.Sum(nil))[:16]
}

func normalizeOAuthCredentialReadOptions(options OAuthCredentialReadOptions) OAuthCredentialReadOptions {
	if options.CredentialsDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			options.CredentialsDir = filepath.Join(home, ".claude")
		}
	}
	if options.SecurityBinary == "" {
		options.SecurityBinary = "security"
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	return options
}

func oauthCredentialStores(options OAuthCredentialReadOptions) []OAuthCredentialStore {
	stores := []OAuthCredentialStore{}
	if runtime.GOOS == "darwin" && options.KeychainService != "" {
		stores = append(stores, keychainOAuthCredentialStore{
			keychainService: options.KeychainService,
			securityBinary:  options.SecurityBinary,
			now:             options.Now,
		})
	}
	stores = append(stores, fileOAuthCredentialStore{
		credentialsDir: options.CredentialsDir,
		now:            options.Now,
	})
	return stores
}

func parseOAuthCredentialsBlob(data []byte, now time.Time, fileMtime int64) (*OAuthTokens, OAuthCredentialMetadata, error) {
	var document oauthCredentialsDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, OAuthCredentialMetadata{FileMtime: fileMtime}, fmt.Errorf("unmarshal credentials: %w", err)
	}
	if document.ClaudeAIOauth == nil {
		return nil, OAuthCredentialMetadata{FileMtime: fileMtime}, nil
	}
	tokens := document.ClaudeAIOauth.Clone()
	metadata := NewOAuthCredentialMetadata(tokens, now, fileMtime)
	return tokens, metadata, nil
}

func oauthTokensExpiredAt(tokens *OAuthTokens, now time.Time) bool {
	if tokens == nil || tokens.AccessToken == "" {
		return true
	}
	if tokens.ExpiresAt == 0 {
		return false
	}
	return !now.Before(time.UnixMilli(tokens.ExpiresAt))
}

func writeHashPart(hash interface{ Write([]byte) (int, error) }, value string) {
	_, _ = hash.Write([]byte(strconv.Itoa(len(value))))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	_, _ = hash.Write([]byte{0})
}
