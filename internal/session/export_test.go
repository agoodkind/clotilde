package session

// DefaultDiscoveryScannersForTest exposes the default scanner set to external
// package tests without adding a production API.
func DefaultDiscoveryScannersForTest(homeDir string) []DiscoveryScanner {
	return defaultDiscoveryScanners(homeDir)
}
