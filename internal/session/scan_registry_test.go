package session_test

import (
	"testing"

	"goodkind.io/clyde/internal/providers/registry"
	"goodkind.io/clyde/internal/session"
)

func TestRegisterDefaultDiscoveryScanners(t *testing.T) {
	registry.RegisterDefaultDiscoveryScanners()

	scanners := session.DefaultDiscoveryScannersForTest(t.TempDir())
	if len(scanners) != 2 {
		t.Fatalf("default scanners = %v, want 2 scanners", scanners)
	}
	if provider := scanners[0].Provider(); provider != session.ProviderClaude {
		t.Fatalf("default scanners[0] provider = %q, want %q", provider, session.ProviderClaude)
	}
	if provider := scanners[1].Provider(); provider != session.ProviderCodex {
		t.Fatalf("default scanners[1] provider = %q, want %q", provider, session.ProviderCodex)
	}

	if _, err := session.ScanProjects(t.TempDir()); err != nil {
		t.Fatalf("ScanProjects returned error after default registration: %v", err)
	}
}
