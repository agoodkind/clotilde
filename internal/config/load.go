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

// loadConfig tries to load config.toml from a directory.
// Uses pelletier/go-toml/v2; the older BurntSushi/toml dep is now unmaintained
// and was removed. Pelletier mirrors the same Marshal / Unmarshal API surface
// so the migration is a one-line import swap on each call.
func loadConfig(dir string) (*Config, error) {
	log := slog.Default().With("concern", "process.daemon.config")
	tomlPath := filepath.Join(dir, "config.toml")
	if util.FileExists(tomlPath) {
		var cfg Config
		data, err := os.ReadFile(tomlPath)
		if err != nil {
			log.Warn("config.load.read_failed",
				"component", "config",
				"subcomponent", "load",
				"path", tomlPath,
				"format", "toml",
				"err", err,
			)
			return nil, fmt.Errorf("failed to read %s: %w", tomlPath, err)
		}
		if err := toml.Unmarshal(data, &cfg); err != nil {
			log.Warn("config.load.parse_failed",
				"component", "config",
				"subcomponent", "load",
				"path", tomlPath,
				"format", "toml",
				"err", err,
			)
			return nil, fmt.Errorf("failed to parse %s: %w", tomlPath, err)
		}
		if err := applyLoggingDefaultsAndValidate(&cfg); err != nil {
			log.Warn("config.load.validate_failed",
				"component", "config",
				"subcomponent", "load",
				"path", tomlPath,
				"format", "toml",
				"err", err,
			)
			return nil, fmt.Errorf("invalid %s: %w", tomlPath, err)
		}
		log.Debug("config.load.loaded",
			"component", "config",
			"subcomponent", "load",
			"format", "toml",
			"path", tomlPath,
		)
		return &cfg, nil
	}

	return nil, os.ErrNotExist
}

// LoadGlobalOrDefault loads the global ~/.config/clyde/ config.
// Returns empty config if config.toml does not exist.
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
// The directory is created if missing.
func SaveGlobal(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("nil config")
	}
	log := slog.Default().With("concern", "process.daemon.config")
	globalDir := filepath.Dir(GlobalConfigPath())
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		log.Warn("config.save.mkdir_failed",
			"component", "config",
			"subcomponent", "save",
			"path", globalDir,
			"err", err,
		)
		return fmt.Errorf("create global config dir: %w", err)
	}
	tomlPath := filepath.Join(globalDir, "config.toml")
	tmp := tomlPath + ".tmp"
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		log.Warn("config.save.encode_failed",
			"component", "config",
			"subcomponent", "save",
			"err", err,
		)
		return fmt.Errorf("encode toml: %w", err)
	}
	if err := os.WriteFile(tmp, encoded, 0o644); err != nil {
		log.Warn("config.save.write_tmp_failed",
			"component", "config",
			"subcomponent", "save",
			"path", tmp,
			"err", err,
		)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, tomlPath); err != nil {
		os.Remove(tmp)
		log.Warn("config.save.rename_failed",
			"component", "config",
			"subcomponent", "save",
			"tmp", tmp,
			"path", tomlPath,
			"err", err,
		)
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
	if logLevel == "" {
		logLevel = "info"
	}
	cfg.Logging.Level = logLevel
	cfg.Logging.Paths.TUI = strings.TrimSpace(cfg.Logging.Paths.TUI)
	cfg.Logging.Paths.Daemon = strings.TrimSpace(cfg.Logging.Paths.Daemon)

	if cfg.Logging.Rotation.MaxSizeMB <= 0 {
		cfg.Logging.Rotation.MaxSizeMB = 5
	}
	if cfg.Logging.Rotation.Enabled == nil {
		v := true
		cfg.Logging.Rotation.Enabled = &v
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

	cfg.MITM.Providers = normalizeMITMProviders(cfg.MITM.Providers)
	switch cfg.MITM.Providers {
	case "both", "claude", "codex":
	default:
		return fmt.Errorf("mitm.providers must be one of both|claude|codex")
	}

	cfg.MITM.BodyMode = normalizeMITMBodyMode(cfg.MITM.BodyMode)
	switch cfg.MITM.BodyMode {
	case "summary", "raw", "off":
	default:
		return fmt.Errorf("mitm.body_mode must be one of summary|raw|off")
	}

	cfg.MITM.CaptureDir = strings.TrimSpace(cfg.MITM.CaptureDir)
	if cfg.MITM.CaptureDir == "" {
		cfg.MITM.CaptureDir = filepath.Join(DefaultStateDir(), "mitm")
	}
	return nil
}

func normalizeMITMProviders(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "both", "all":
		return "both"
	case "claude":
		return "claude"
	case "codex":
		return "codex"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

func normalizeMITMBodyMode(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "summary":
		return "summary"
	case "raw":
		return "raw"
	case "off":
		return "off"
	default:
		return strings.ToLower(strings.TrimSpace(v))
	}
}

// MergedProfiles helper removed; callers now use LoadGlobalOrDefault and project
// config loading inline at their callsites.
