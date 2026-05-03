package oauthcredentials

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadCandidates_ReadsFileCredential(t *testing.T) {
	dir := t.TempDir()
	writeTestCredentials(t, dir, &Tokens{
		AccessToken:  "access-file-secret",
		RefreshToken: "refresh-file-secret",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
		Scopes:       []string{"user:profile", "user:inference"},
	})

	results := ReadCandidates(context.Background(), ReadOptions{
		CredentialsDir: dir,
		Now:            time.Now(),
	})
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	result := results[0]
	if result.Source != SourceFile {
		t.Fatalf("source = %q, want %q", result.Source, SourceFile)
	}
	if result.Err != nil {
		t.Fatalf("read err = %v, want nil", result.Err)
	}
	if !result.Present || result.Tokens == nil {
		t.Fatalf("present = %t tokens nil = %t, want present tokens", result.Present, result.Tokens == nil)
	}
	if !result.Metadata.AccessTokenPresent || !result.Metadata.RefreshTokenPresent {
		t.Fatalf("metadata token presence = %+v, want both true", result.Metadata)
	}
	if result.Metadata.Fingerprint == "" {
		t.Fatal("fingerprint is empty")
	}
	summaries := Summarize(results)
	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("marshal summaries: %v", err)
	}
	encodedText := string(encoded)
	if strings.Contains(encodedText, "access-file-secret") || strings.Contains(encodedText, "refresh-file-secret") {
		t.Fatalf("summary leaked token value: %s", encodedText)
	}
}

func TestWriteFile_PreservesOtherKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".credentials.json")
	initial := []byte(`{"mcpOAuth":{"server":{"accessToken":"other-secret"}}}`)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatalf("write initial credentials: %v", err)
	}
	tokens := &Tokens{
		AccessToken:  "new-access-secret",
		RefreshToken: "new-refresh-secret",
		ExpiresAt:    time.Now().Add(time.Hour).UnixMilli(),
	}
	if err := WriteFile(context.Background(), dir, tokens); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read credentials: %v", err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	if _, ok := document["mcpOAuth"]; !ok {
		t.Fatal("mcpOAuth key was not preserved")
	}
	if _, ok := document["claudeAiOauth"]; !ok {
		t.Fatal("claudeAiOauth key was not written")
	}
}

func writeTestCredentials(t *testing.T, dir string, tokens *Tokens) {
	t.Helper()
	encoded, err := json.Marshal(struct {
		ClaudeAIOauth *Tokens `json:"claudeAiOauth"`
	}{ClaudeAIOauth: tokens})
	if err != nil {
		t.Fatalf("marshal test credentials: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), encoded, 0o600); err != nil {
		t.Fatalf("write test credentials: %v", err)
	}
}
