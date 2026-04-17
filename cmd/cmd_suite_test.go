package cmd_test

import (
	"os"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

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

	// Some specs shell out to the real clotilde binary which re-reads
	// these values; the env inheritance handles them.
	for _, sub := range []string{"data", "cache", "config"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", sub, err)
		}
	}

	RunSpecs(t, "Cmd Suite")
}
