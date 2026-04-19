package hook_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = BeforeEach(func() {
	spec := GinkgoT().TempDir()
	for _, sub := range []string{"data", "cache", "config", "state", "run"} {
		_ = os.MkdirAll(filepath.Join(spec, sub), 0o700)
	}
	GinkgoT().Setenv("XDG_DATA_HOME", filepath.Join(spec, "data"))
	GinkgoT().Setenv("XDG_CACHE_HOME", filepath.Join(spec, "cache"))
	GinkgoT().Setenv("XDG_CONFIG_HOME", filepath.Join(spec, "config"))
	GinkgoT().Setenv("XDG_STATE_HOME", filepath.Join(spec, "state"))
	GinkgoT().Setenv("XDG_RUNTIME_DIR", filepath.Join(spec, "run"))
})

func TestHookCLI(t *testing.T) {
	RegisterFailHandler(Fail)

	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	t.Setenv("CLYDE_DISABLE_DAEMON", "1")

	for _, sub := range []string{"data", "cache", "config", "state", "run"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	RunSpecs(t, "Hook CLI Suite")
}
