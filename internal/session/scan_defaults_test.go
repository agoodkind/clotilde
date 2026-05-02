package session

import "testing"

func TestDefaultDiscoveryScanners(t *testing.T) {
	if scanners := defaultDiscoveryScanners(""); scanners != nil {
		t.Fatalf("defaultDiscoveryScanners(\"\") = %v, want nil", scanners)
	}

	scanners := defaultDiscoveryScanners("/tmp/home")
	if len(scanners) != 2 {
		t.Fatalf("defaultDiscoveryScanners returned %d scanners, want 2", len(scanners))
	}
	if provider := scanners[0].Provider(); provider != ProviderClaude {
		t.Fatalf("defaultDiscoveryScanners provider = %q, want %q", provider, ProviderClaude)
	}
	if provider := scanners[1].Provider(); provider != ProviderCodex {
		t.Fatalf("defaultDiscoveryScanners provider = %q, want %q", provider, ProviderCodex)
	}
}
