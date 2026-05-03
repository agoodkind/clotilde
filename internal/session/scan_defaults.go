package session

import "strings"

// defaultDiscoveryScanners returns the registered provider scanner set for the
// current installation. The caller owns cache construction and refresh policy.
func defaultDiscoveryScanners(homeDir string) []DiscoveryScanner {
	return registeredDiscoveryScanners(homeDir)
}

func defaultDiscoveryCache(homeDir string) *discoveryCache {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return nil
	}
	return newDiscoveryCache(defaultDiscoveryScanners(homeDir), 0)
}
