// Package compact rebuilds the `clyde compact` command on top of an
// append-only model that mirrors Claude Code's own /compact writer.
//
// Every public function in this package is pure or scoped to one
// well-defined IO boundary so the orchestrator (plan.go) can drive it
// without holding shared state. See the rebuild plan for the design.
package compact

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// AnthropicAPIKey reads the Anthropic API key from the user's global
// clyde config at ~/.config/clyde/config.toml under
// [defaults] anthropic_api_key.
//
// IMPORTANT: This key is NOT used to authenticate claude -p invocations.
// Claude Code authenticates via OAuth tokens (cached under ~/.claude/)
// against the user's Claude Max subscription buckets, not via direct
// Anthropic API key billing. This key is used solely for the free
// /v1/messages/count_tokens endpoint, which the local token-count
// helpers and the transcript verifier hit to get an authoritative
// figure that matches what `/context` reports inside Claude Code.
// Removing it disables exact token counts and falls back to local
// tiktoken estimates; it does NOT break adapter or session spawning.
//
// The value is never logged or returned
// in error messages so accidental tracing cannot leak it.
//
// Returns ErrNoAPIKey when the config exists but the key is empty so
// callers can distinguish "user must configure" from "transient IO
// error" without parsing strings.
func AnthropicAPIKey() (string, error) {
	path, err := globalConfigPath()
	if err != nil {
		return "", fmt.Errorf("resolve global config path: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrNoConfig
		}
		return "", fmt.Errorf("read global config: %w", err)
	}
	var cfg struct {
		Defaults struct {
			AnthropicAPIKey string `toml:"anthropic_api_key"`
		} `toml:"defaults"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse global config: %w", err)
	}
	key := strings.TrimSpace(cfg.Defaults.AnthropicAPIKey)
	if key == "" {
		return "", ErrNoAPIKey
	}
	return key, nil
}

// ErrNoConfig is returned when ~/.config/clyde/config.toml does not
// exist. Callers can present a setup hint instead of a stack trace.
var ErrNoConfig = fmt.Errorf("clyde global config not found")

// ErrNoAPIKey is returned when the config exists but defaults
// anthropic_api_key is empty.
var ErrNoAPIKey = fmt.Errorf("anthropic_api_key not set in clyde global config")

// globalConfigPath returns the absolute path to
// ~/.config/clyde/config.toml, honoring XDG_CONFIG_HOME.
func globalConfigPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "clyde", "config.toml"), nil
}
