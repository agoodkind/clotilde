package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// BeforeEach at the suite scope swaps XDG_DATA_HOME per spec so
// sessions created by one test never leak into the next. Without this
// every spec sees whatever the previous spec left behind in the shared
// XDG root set by TestCmd.
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

// TestCmd is the single entry point for every Ginkgo spec in this package.
//
// Before handing off to Ginkgo, it redirects XDG_DATA_HOME (and
// XDG_CACHE_HOME, XDG_CONFIG_HOME for good measure) into a throwaway
// directory under the test's private t.TempDir. This prevents every
// session created by a test from writing into the real user's
// ~/.local/share/clotilde/sessions/ store. Prior to this, each `go test`
// run left 20+ orphan "sessions" in the user's dashboard, visible forever
// until they were manually deleted with `clotilde prune-ephemeral`.
//
// Ginkgo runs all specs inside this single Go test, so setting the env
// once here covers the whole suite.
func TestCmd(t *testing.T) {
	RegisterFailHandler(Fail)

	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(root, "run"))
	// Disable daemon in tests. The real daemon running under launchd
	// would otherwise inject its own --settings flag into the wrapper
	// argv, defeating tests that assert flag composition.
	t.Setenv("CLOTILDE_DISABLE_DAEMON", "1")

	// Some specs shell out to the real clotilde binary which re-reads
	// these values; the env inheritance handles them.
	for _, sub := range []string{"data", "cache", "config", "state", "run"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	RunSpecs(t, "Cmd Suite")
}
