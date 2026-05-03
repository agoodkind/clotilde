package oauthcredentials

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"
)

type keychainStore struct {
	keychainService string
	securityBinary  string
	now             time.Time
}

func (s keychainStore) Source() Source {
	return SourceKeychain
}

func (s keychainStore) Read(ctx context.Context) ReadResult {
	cmd := exec.CommandContext(ctx, s.securityBinary, "find-generic-password", "-s", s.keychainService, "-w")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return ReadResult{
			Source: SourceKeychain,
			Err:    fmt.Errorf("security find-generic-password: %w", err),
		}
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return ReadResult{Source: SourceKeychain}
	}
	tokens, metadata, parseErr := parseBlob(out, s.now, 0)
	return ReadResult{
		Source:   SourceKeychain,
		Tokens:   tokens,
		Present:  tokens != nil,
		Err:      parseErr,
		Metadata: metadata,
	}
}

func (s keychainStore) Write(_ context.Context, _ *Tokens) WriteResult {
	return WriteResult{
		Source: SourceKeychain,
		Err:    fmt.Errorf("keychain OAuth credential writes are not implemented"),
	}
}
