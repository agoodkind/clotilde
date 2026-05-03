package session

import (
	"errors"
	"maps"
	"testing"
)

type defaultDiscoveryTestScanner struct {
	provider ProviderID
}

func (scanner defaultDiscoveryTestScanner) Provider() ProviderID {
	return scanner.provider
}

func (scanner defaultDiscoveryTestScanner) Scan() ([]DiscoveryResult, error) {
	return nil, nil
}

func preserveDiscoveryRegistry(t *testing.T) {
	t.Helper()

	discoveryRegistry.mu.Lock()
	previousScanners := make(map[ProviderID]DiscoveryScanner, len(discoveryRegistry.scanners))
	maps.Copy(previousScanners, discoveryRegistry.scanners)
	discoveryRegistry.scanners = make(map[ProviderID]DiscoveryScanner)
	discoveryRegistry.mu.Unlock()

	t.Cleanup(func() {
		discoveryRegistry.mu.Lock()
		discoveryRegistry.scanners = previousScanners
		discoveryRegistry.mu.Unlock()
	})
}

func TestDefaultDiscoveryScanners(t *testing.T) {
	preserveDiscoveryRegistry(t)

	if scanners := defaultDiscoveryScanners(""); scanners != nil {
		t.Fatalf("defaultDiscoveryScanners(\"\") = %v, want nil", scanners)
	}

	RegisterDiscoveryScanner(ProviderCodex, defaultDiscoveryTestScanner{provider: ProviderCodex})
	RegisterDiscoveryScanner(ProviderClaude, defaultDiscoveryTestScanner{provider: ProviderClaude})

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

func TestScanProjectsReturnsMissingRegistrationError(t *testing.T) {
	preserveDiscoveryRegistry(t)

	_, err := ScanProjects(t.TempDir())
	if err == nil {
		t.Fatal("ScanProjects succeeded, want missing registration error")
	}
	var missingRegistrationError ErrDiscoveryScannerNotRegistered
	if !errors.As(err, &missingRegistrationError) {
		t.Fatalf("ScanProjects error = %T, want %T", err, ErrDiscoveryScannerNotRegistered{})
	}
	if missingRegistrationError.Provider != ProviderClaude {
		t.Fatalf("ScanProjects error provider = %q, want %q", missingRegistrationError.Provider, ProviderClaude)
	}
}
