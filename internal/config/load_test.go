package config_test

import (
	"encoding/json"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"goodkind.io/clyde/internal/config"
)

var _ = Describe("NewConfig", func() {
	It("should create config with defaults", func() {
		cfg := config.NewConfig()
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Profiles).To(BeEmpty())
	})
})

var _ = Describe("LoadGlobalOrDefault", func() {
	var origXDG string

	BeforeEach(func() {
		origXDG = os.Getenv("XDG_CONFIG_HOME")
	})

	AfterEach(func() {
		if origXDG == "" {
			_ = os.Unsetenv("XDG_CONFIG_HOME")
		} else {
			_ = os.Setenv("XDG_CONFIG_HOME", origXDG)
		}
	})

	It("returns empty config when file is absent", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		cfg, err := config.LoadGlobalOrDefault()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg).NotTo(BeNil())
		Expect(cfg.Profiles).To(BeEmpty())
		Expect(cfg.Logging.Level).To(BeEmpty())
		Expect(cfg.Logging.Rotation.Enabled).NotTo(BeNil())
		Expect(*cfg.Logging.Rotation.Enabled).To(BeTrue())
		Expect(cfg.Logging.Rotation.MaxSizeMB).To(Equal(5))
		Expect(cfg.Logging.Rotation.MaxBackups).To(Equal(5))
		Expect(cfg.Logging.Rotation.MaxAgeDays).To(Equal(14))
		Expect(cfg.Logging.Rotation.Compress).NotTo(BeNil())
		Expect(*cfg.Logging.Rotation.Compress).To(BeTrue())
		Expect(cfg.Logging.Body.Mode).To(Equal("summary"))
		Expect(cfg.Logging.Body.MaxKB).To(Equal(32))
	})

	It("loads profiles correctly when file is present", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"profiles": map[string]any{
				"quick": map[string]string{"model": "haiku"},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		cfg, err := config.LoadGlobalOrDefault()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Profiles["quick"].Model).To(Equal("haiku"))
	})

	It("applies logging defaults when logging stanza is omitted", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"profiles": map[string]any{
				"quick": map[string]string{"model": "haiku"},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		cfg, err := config.LoadGlobalOrDefault()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Logging.Rotation.Enabled).NotTo(BeNil())
		Expect(*cfg.Logging.Rotation.Enabled).To(BeTrue())
		Expect(cfg.Logging.Rotation.MaxSizeMB).To(Equal(5))
		Expect(cfg.Logging.Rotation.MaxBackups).To(Equal(5))
		Expect(cfg.Logging.Rotation.MaxAgeDays).To(Equal(14))
		Expect(cfg.Logging.Rotation.Compress).NotTo(BeNil())
		Expect(*cfg.Logging.Rotation.Compress).To(BeTrue())
		Expect(cfg.Logging.Body.Mode).To(Equal("summary"))
		Expect(cfg.Logging.Body.MaxKB).To(Equal(32))
	})

	It("rejects invalid logging.body.mode", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"logging": map[string]any{
				"body": map[string]any{
					"mode": "bogus",
				},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		_, err := config.LoadGlobalOrDefault()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("logging.body.mode must be one of summary|whitelist|raw|off"))
	})

	It("accepts logging.rotation.enabled = false", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"logging": map[string]any{
				"rotation": map[string]any{
					"enabled": false,
				},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		cfg, err := config.LoadGlobalOrDefault()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Logging.Rotation.Enabled).NotTo(BeNil())
		Expect(*cfg.Logging.Rotation.Enabled).To(BeFalse())
	})

	It("loads logging.paths for daemon and tui", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"logging": map[string]any{
				"paths": map[string]any{
					"daemon": "/tmp/clyde-daemon.jsonl",
					"tui":    "/tmp/clyde-tui.jsonl",
				},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		cfg, err := config.LoadGlobalOrDefault()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.Logging.Paths.Daemon).To(Equal("/tmp/clyde-daemon.jsonl"))
		Expect(cfg.Logging.Paths.TUI).To(Equal("/tmp/clyde-tui.jsonl"))
	})

	It("rejects negative logging.rotation.max_backups", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"logging": map[string]any{
				"rotation": map[string]any{
					"max_backups": -1,
				},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		_, err := config.LoadGlobalOrDefault()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("logging.rotation.max_backups must be >= 0"))
	})

	It("rejects invalid logging.body.max_kb", func() {
		tmpDir := GinkgoT().TempDir()
		_ = os.Setenv("XDG_CONFIG_HOME", tmpDir)

		globalDir := filepath.Join(tmpDir, "clyde")
		Expect(os.MkdirAll(globalDir, 0o755)).To(Succeed())
		data, _ := json.Marshal(map[string]any{
			"logging": map[string]any{
				"body": map[string]any{
					"max_kb": 300,
				},
			},
		})
		Expect(os.WriteFile(filepath.Join(globalDir, "config.json"), data, 0o644)).To(Succeed())

		_, err := config.LoadGlobalOrDefault()
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("logging.body.max_kb must be between 1 and 256"))
	})
})
