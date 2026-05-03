package claude

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type keychainOAuthCredentialStore struct {
	keychainService string
	securityBinary  string
	now             time.Time
}

func (s keychainOAuthCredentialStore) Source() OAuthCredentialSource {
	return OAuthCredentialSourceKeychain
}

func (s keychainOAuthCredentialStore) Read(ctx context.Context) OAuthCredentialReadResult {
	cmd := exec.CommandContext(ctx, s.securityBinary, "find-generic-password", "-s", s.keychainService, "-w")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return OAuthCredentialReadResult{
			Source: OAuthCredentialSourceKeychain,
			Err:    fmt.Errorf("security find-generic-password: %w", err),
		}
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return OAuthCredentialReadResult{Source: OAuthCredentialSourceKeychain}
	}
	tokens, metadata, parseErr := parseOAuthCredentialsBlob(out, s.now, 0)
	return OAuthCredentialReadResult{
		Source:   OAuthCredentialSourceKeychain,
		Tokens:   tokens,
		Present:  tokens != nil,
		Err:      parseErr,
		Metadata: metadata,
	}
}

func (s keychainOAuthCredentialStore) Write(_ context.Context, _ *OAuthTokens) OAuthCredentialWriteResult {
	return OAuthCredentialWriteResult{
		Source: OAuthCredentialSourceKeychain,
		Err:    fmt.Errorf("keychain OAuth credential writes are not implemented"),
	}
}
