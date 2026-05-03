package session_test

import (
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"goodkind.io/clyde/internal/providers/registry"
)

func TestMain(m *testing.M) {
	registry.RegisterDefaultDiscoveryScanners()
	os.Exit(m.Run())
}

func TestSession(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Session Suite")
}
