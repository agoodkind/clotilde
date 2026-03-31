package config

import (
	"fmt"
	"maps"
	"os"

	"github.com/fgrehm/clotilde/internal/util"
)

// Load reads the config.json file from the clotilde root.
// Returns a Config struct or an error if reading/parsing fails.
func Load(clotildeRoot string) (*Config, error) {
	configPath := GetConfigPath(clotildeRoot)
	var cfg Config
	if err := util.ReadJSON(configPath, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadOrDefault loads the config, or returns a default config if it doesn't exist.
// Returns an error only if the file exists but can't be read/parsed.
func LoadOrDefault(clotildeRoot string) (*Config, error) {
	cfg, err := Load(clotildeRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return cfg, nil
}

// LoadGlobalOrDefault loads the global ~/.config/clotilde/config.json.
// Returns empty config if the file doesn't exist.
func LoadGlobalOrDefault() (*Config, error) {
	var cfg Config
	if err := util.ReadJSON(GlobalConfigPath(), &cfg); err != nil {
		if os.IsNotExist(err) {
			return NewConfig(), nil
		}
		return nil, err
	}
	return &cfg, nil
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
