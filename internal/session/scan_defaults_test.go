package session

import "testing"

func TestDefaultDiscoveryScanners(t *testing.T) {
	if scanners := defaultDiscoveryScanners(""); scanners != nil {
		t.Fatalf("defaultDiscoveryScanners(\"\") = %v, want nil", scanners)
	}

	scanners := defaultDiscoveryScanners("/tmp/home")
	if len(scanners) != 1 {
		t.Fatalf("defaultDiscoveryScanners returned %d scanners, want 1", len(scanners))
	}
	if provider := scanners[0].Provider(); provider != ProviderClaude {
		t.Fatalf("defaultDiscoveryScanners provider = %q, want %q", provider, ProviderClaude)
	}
}
