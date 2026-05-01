package session

import (
	"strings"

	"goodkind.io/clyde/internal/config"
)

// defaultDiscoveryScanners returns the built-in provider scanner set for the
// current installation. The caller owns cache construction and refresh policy.
func defaultDiscoveryScanners(homeDir string) []DiscoveryScanner {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return nil
	}
	return []DiscoveryScanner{
		newClaudeDiscoveryScanner(config.ClaudeProjectsRoot(homeDir)),
	}
}

func defaultDiscoveryCache(homeDir string) *discoveryCache {
	return newDiscoveryCache(defaultDiscoveryScanners(homeDir), 0)
}
