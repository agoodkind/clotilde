// Package oauth manages adapter OAuth token flows and persistence.
package oauth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// readCredentials returns the parsed Tokens from whichever store is
// available. macOS prefers Keychain when keychainService is non-empty;
// on read failure we fall through to ~/.claude/.credentials.json.
func readCredentials(dir, keychainService string) (*Tokens, error) {
	if runtime.GOOS == "darwin" && keychainService != "" {
		if t, err := readKeychain(keychainService); err == nil && t != nil {
			return t, nil
		}
	}
	return readCredentialsFile(filepath.Join(dir, ".credentials.json"))
}

func readKeychain(keychainService string) (*Tokens, error) {
	cmd := exec.Command("security", "find-generic-password",
		"-s", keychainService, "-w")
	cmd.Stderr = io.Discard
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("security find-generic-password: %w", err)
	}
	out = bytes.TrimSpace(out)
	return parseCredentialsBlob(out)
}

func readCredentialsFile(path string) (*Tokens, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return parseCredentialsBlob(data)
}

func parseCredentialsBlob(data []byte) (*Tokens, error) {
	var doc credentialsDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	return doc.ClaudeAIOauth, nil
}

// writeCredentials updates the on-disk credentials file with new
// claudeAiOauth tokens, preserving any other top-level keys (mcpOAuth
// etc) that the CLI might have written. We do not write to keychain
// directly; the next `claude` CLI invocation will detect the file
// mtime change and resync (per the CLI's own
// invalidateOAuthCacheIfDiskChanged logic).
func writeCredentials(dir string, tokens *Tokens) error {
	credsPath := filepath.Join(dir, ".credentials.json")

	merged := map[string]json.RawMessage{}
	if existing, err := os.ReadFile(credsPath); err == nil {
		_ = json.Unmarshal(existing, &merged)
	}

	encoded, err := json.Marshal(tokens)
	if err != nil {
		return fmt.Errorf("marshal tokens: %w", err)
	}
	merged["claudeAiOauth"] = encoded

	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged credentials: %w", err)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("mkdir credentials dir: %w", err)
	}
	tmp := credsPath + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return fmt.Errorf("write temp credentials: %w", err)
	}
	if err := os.Rename(tmp, credsPath); err != nil {
		return fmt.Errorf("rename temp credentials: %w", err)
	}
	return nil
}

func coalesce(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func splitScopes(raw string, fallback []string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	return strings.Fields(raw)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
