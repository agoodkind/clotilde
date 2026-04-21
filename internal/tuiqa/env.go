package tuiqa

import (
	"fmt"
	"os"
	"path/filepath"
)

// IsolatedXDG returns environment lines KEY=value for a hermetic tree under root.
// Layout: root/data, root/config, root/cache, root/state, root/runtime.
func IsolatedXDG(root string) []string {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	data := filepath.Join(abs, "data")
	cfg := filepath.Join(abs, "config")
	cache := filepath.Join(abs, "cache")
	state := filepath.Join(abs, "state")
	runtime := filepath.Join(abs, "runtime")
	return []string{
		"XDG_DATA_HOME=" + data,
		"XDG_CONFIG_HOME=" + cfg,
		"XDG_CACHE_HOME=" + cache,
		"XDG_STATE_HOME=" + state,
		"XDG_RUNTIME_DIR=" + runtime,
		"HOME=" + abs,
		"TERM=xterm-256color",
	}
}

// PrepareIsolatedRoot creates directories for IsolatedXDG and sets runtime to 0700.
func PrepareIsolatedRoot(root string) error {
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("abs path: %w", err)
	}
	sub := []string{"data", "config", "cache", "state", "runtime"}
	for _, name := range sub {
		p := filepath.Join(abs, name)
		mode := os.FileMode(0o755)
		if name == "runtime" {
			mode = 0o700
		}
		if err := os.MkdirAll(p, mode); err != nil {
			return fmt.Errorf("mkdir %s: %w", p, err)
		}
	}
	return nil
}
