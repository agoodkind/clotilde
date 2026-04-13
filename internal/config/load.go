package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/fgrehm/clotilde/internal/util"
)

// loadConfig tries to load config from a directory, preferring .toml over .json.
func loadConfig(dir string) (*Config, error) {
	// Prefer TOML
	tomlPath := filepath.Join(dir, "config.toml")
	if util.FileExists(tomlPath) {
		var cfg Config
		if _, err := toml.DecodeFile(tomlPath, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", tomlPath, err)
		}
		return &cfg, nil
	}

	// Fall back to JSON
	jsonPath := filepath.Join(dir, "config.json")
	if util.FileExists(jsonPath) {
		var cfg Config
		if err := util.ReadJSON(jsonPath, &cfg); err != nil {
			return nil, err
		}
		return &cfg, nil
	}

	return nil, os.ErrNotExist
}

// Load reads the config file from the clotilde root.
// Prefers config.toml over config.json.
func Load(clotildeRoot string) (*Config, error) {
	return loadConfig(clotildeRoot)
}

// LoadOrDefault loads the config, or returns a default config if it doesn't exist.
// Returns an error only if the file exists but can't be read/parsed.
func LoadOrDefault(clotildeRoot string) (*Config, error) {
	cfg, err := Load(clotildeRoot)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// LoadGlobalOrDefault loads the global ~/.config/clotilde/ config.
// Prefers config.toml over config.json. Returns empty config if neither exists.
func LoadGlobalOrDefault() (*Config, error) {
	globalDir := filepath.Dir(GlobalConfigPath()) // ~/.config/clotilde/
	cfg, err := loadConfig(globalDir)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// MergedProfiles returns a profile map combining global and project configs.
// Project-level profiles take precedence over global ones with the same name.
func MergedProfiles(clotildeRoot string) (map[string]Profile, error) {
	globalCfg, err := LoadGlobalOrDefault()
	if err != nil {
		return nil, fmt.Errorf("failed to load global config: %w", err)
	}
	projectCfg, err := LoadOrDefault(clotildeRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to load project config: %w", err)
	}

	merged := make(map[string]Profile)
	maps.Copy(merged, globalCfg.Profiles)
	// project overrides global
	maps.Copy(merged, projectCfg.Profiles)
	return merged, nil
}
