package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type fileOAuthCredentialStore struct {
	credentialsDir string
	now            time.Time
}

func (s fileOAuthCredentialStore) Source() OAuthCredentialSource {
	return OAuthCredentialSourceFile
}

func (s fileOAuthCredentialStore) Read(_ context.Context) OAuthCredentialReadResult {
	path := filepath.Join(s.credentialsDir, ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OAuthCredentialReadResult{Source: OAuthCredentialSourceFile}
		}
		return OAuthCredentialReadResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("read %s: %w", path, err),
		}
	}
	fileMtime := int64(0)
	if info, statErr := os.Stat(path); statErr == nil {
		fileMtime = info.ModTime().UnixNano()
	}
	tokens, metadata, parseErr := parseOAuthCredentialsBlob(data, s.now, fileMtime)
	return OAuthCredentialReadResult{
		Source:   OAuthCredentialSourceFile,
		Tokens:   tokens,
		Present:  tokens != nil,
		Err:      parseErr,
		Metadata: metadata,
	}
}

func (s fileOAuthCredentialStore) Write(_ context.Context, tokens *OAuthTokens) OAuthCredentialWriteResult {
	if tokens == nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("write credentials: tokens are nil"),
		}
	}
	credsPath := filepath.Join(s.credentialsDir, ".credentials.json")
	merged := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(credsPath); err == nil {
		_ = json.Unmarshal(existing, &merged)
	}
	encoded, err := json.Marshal(tokens)
	if err != nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("marshal tokens: %w", err),
		}
	}
	merged["claudeAiOauth"] = encoded
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("marshal merged credentials: %w", err),
		}
	}
	if err := os.MkdirAll(s.credentialsDir, 0o700); err != nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("mkdir credentials dir: %w", err),
		}
	}
	tmp := credsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("write temp credentials: %w", err),
		}
	}
	if err := os.Rename(tmp, credsPath); err != nil {
		return OAuthCredentialWriteResult{
			Source: OAuthCredentialSourceFile,
			Err:    fmt.Errorf("rename temp credentials: %w", err),
		}
	}
	return OAuthCredentialWriteResult{Source: OAuthCredentialSourceFile}
}
