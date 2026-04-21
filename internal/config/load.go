package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"goodkind.io/clyde/internal/util"
)

// loadConfig tries to load config from a directory, preferring .toml over .json.
// Uses pelletier/go-toml/v2; the older BurntSushi/toml dep is now unmaintained
// and was removed. Pelletier mirrors the same Marshal / Unmarshal API surface
// so the migration is a one-line import swap on each call.
func loadConfig(dir string) (*Config, error) {
	// Prefer TOML
	tomlPath := filepath.Join(dir, "config.toml")
	if util.FileExists(tomlPath) {
		var cfg Config
		data, err := os.ReadFile(tomlPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", tomlPath, err)
		}
		if err := toml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", tomlPath, err)
		}
		if err := applyLoggingDefaultsAndValidate(&cfg); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", tomlPath, err)
		}
		slog.Info("config.load.loaded",
			"component", "config",
			"subcomponent", "load",
			"format", "toml",
			"path", tomlPath,
		)
		return &cfg, nil
	}

	// Fall back to JSON
	jsonPath := filepath.Join(dir, "config.json")
	if util.FileExists(jsonPath) {
		var cfg Config
		if err := util.ReadJSON(jsonPath, &cfg); err != nil {
			return nil, err
		}
		if err := applyLoggingDefaultsAndValidate(&cfg); err != nil {
			return nil, err
		}
		slog.Info("config.load.loaded",
			"component", "config",
			"subcomponent", "load",
			"format", "json",
			"path", jsonPath,
		)
		return &cfg, nil
	}

	return nil, os.ErrNotExist
}

// LoadGlobalOrDefault loads the global ~/.config/clyde/ config.
// Prefers config.toml over config.json. Returns empty config if neither exists.
func LoadGlobalOrDefault() (*Config, error) {
	globalDir := filepath.Dir(GlobalConfigPath()) // ~/.config/clyde/
	cfg, err := loadConfig(globalDir)
	if err != nil {
		if os.IsNotExist(err) || strings.Contains(err.Error(), "no such file") {
			return NewConfigWithDefaults(), nil
		}
		return nil, err
	}
	if err := applyLoggingDefaultsAndValidate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// SaveGlobal writes the config back to the global location as TOML.
// The directory is created if missing. Existing JSON files are not
// migrated; callers can delete the JSON manually after the TOML lands.
func SaveGlobal(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	globalDir := filepath.Dir(GlobalConfigPath())
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		return fmt.Errorf("create global config dir: %w", err)
	}
	tomlPath := filepath.Join(globalDir, "config.toml")
	tmp := tomlPath + ".tmp"
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, tomlPath); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

func NewConfigWithDefaults() *Config {
	cfg := NewConfig()
	_ = applyLoggingDefaultsAndValidate(cfg)
	return cfg
}

func applyLoggingDefaultsAndValidate(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	logLevel := strings.ToLower(strings.TrimSpace(cfg.Logging.Level))
	cfg.Logging.Level = logLevel

	if cfg.Logging.Rotation.MaxSizeMB <= 0 {
		cfg.Logging.Rotation.MaxSizeMB = 5
	}
	if cfg.Logging.Rotation.MaxBackups < 0 {
		return fmt.Errorf("logging.rotation.max_backups must be >= 0")
	}
	if cfg.Logging.Rotation.MaxBackups == 0 {
		cfg.Logging.Rotation.MaxBackups = 5
	}
	if cfg.Logging.Rotation.MaxAgeDays < 0 {
		return fmt.Errorf("logging.rotation.max_age_days must be >= 0")
	}
	if cfg.Logging.Rotation.MaxAgeDays == 0 {
		cfg.Logging.Rotation.MaxAgeDays = 14
	}
	if cfg.Logging.Rotation.Compress == nil {
		v := true
		cfg.Logging.Rotation.Compress = &v
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Logging.Body.Mode))
	if mode == "" {
		mode = "summary"
	}
	cfg.Logging.Body.Mode = mode

	if cfg.Logging.Body.MaxKB <= 0 {
		cfg.Logging.Body.MaxKB = 32
	}
	if cfg.Logging.Body.MaxKB > 256 {
		return fmt.Errorf("logging.body.max_kb must be between 1 and 256")
	}
	switch cfg.Logging.Body.Mode {
	case "", "summary", "whitelist", "raw", "off":
	default:
		return fmt.Errorf("logging.body.mode must be one of summary|whitelist|raw|off")
	}
	if cfg.Logging.Body.Mode == "" {
		cfg.Logging.Body.Mode = "summary"
	}
	return nil
}

// MergedProfiles helper removed; callers now use LoadGlobalOrDefault and project
// config loading inline at their callsites.
