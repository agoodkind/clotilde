package session

import (
	"sort"
	"strings"
	"sync"
)

type homeDiscoveryScanner interface {
	DiscoveryScannerForHome(homeDir string) DiscoveryScanner
}

type rootDiscoveryScanner interface {
	DiscoveryScannerForRoot(root string) DiscoveryScanner
}

var discoveryRegistry = struct {
	mu       sync.RWMutex
	scanners map[ProviderID]DiscoveryScanner
}{
	scanners: make(map[ProviderID]DiscoveryScanner),
}

// RegisterDiscoveryScanner adds or replaces the discovery scanner for provider.
// Provider-aware entrypoints call this explicitly so the session domain can
// build scanner sets without importing provider implementations.
func RegisterDiscoveryScanner(provider ProviderID, scanner DiscoveryScanner) {
	provider = NormalizeProviderID(provider)
	if provider == ProviderUnknown || scanner == nil {
		return
	}
	discoveryRegistry.mu.Lock()
	defer discoveryRegistry.mu.Unlock()
	discoveryRegistry.scanners[provider] = scanner
}

func registeredDiscoveryScanners(homeDir string) []DiscoveryScanner {
	homeDir = strings.TrimSpace(homeDir)
	if homeDir == "" {
		return nil
	}
	discoveryRegistry.mu.RLock()
	defer discoveryRegistry.mu.RUnlock()

	providers := registeredDiscoveryScannerProvidersLocked()
	scanners := make([]DiscoveryScanner, 0, len(providers))
	for _, provider := range providers {
		scanner := discoveryRegistry.scanners[provider]
		if homeScanner, ok := scanner.(homeDiscoveryScanner); ok {
			scanner = homeScanner.DiscoveryScannerForHome(homeDir)
		}
		if scanner != nil {
			scanners = append(scanners, scanner)
		}
	}
	return scanners
}

func registeredDiscoveryScannerProvidersLocked() []ProviderID {
	providers := make([]ProviderID, 0, len(discoveryRegistry.scanners))
	for provider := range discoveryRegistry.scanners {
		providers = append(providers, provider)
	}
	sort.Slice(providers, func(i int, j int) bool {
		return discoveryProviderSortKey(providers[i]) < discoveryProviderSortKey(providers[j])
	})
	return providers
}

func discoveryProviderSortKey(provider ProviderID) string {
	switch NormalizeProviderID(provider) {
	case ProviderClaude:
		return "00:claude"
	case ProviderCodex:
		return "01:codex"
	default:
		return "99:" + string(provider)
	}
}

func discoveryScannerForRoot(provider ProviderID, root string) (DiscoveryScanner, bool) {
	provider = NormalizeProviderID(provider)
	root = strings.TrimSpace(root)
	if provider == ProviderUnknown || root == "" {
		return nil, false
	}
	discoveryRegistry.mu.RLock()
	scanner := discoveryRegistry.scanners[provider]
	discoveryRegistry.mu.RUnlock()
	if scanner == nil {
		return nil, false
	}
	if rootScanner, ok := scanner.(rootDiscoveryScanner); ok {
		scanner = rootScanner.DiscoveryScannerForRoot(root)
	}
	return scanner, scanner != nil
}

// ScanProjects scans a Claude projects root through the registered Claude
// discovery scanner. It remains as a compatibility entry point while callers
// move to provider-neutral scanner sets.
func ScanProjects(claudeProjectsDir string) ([]DiscoveryResult, error) {
	scanner, ok := discoveryScannerForRoot(ProviderClaude, claudeProjectsDir)
	if !ok {
		return nil, ErrDiscoveryScannerNotRegistered{Provider: ProviderClaude}
	}
	return scanner.Scan()
}

type ErrDiscoveryScannerNotRegistered struct {
	Provider ProviderID
}

func (err ErrDiscoveryScannerNotRegistered) Error() string {
	return "discovery scanner not registered: " + string(NormalizeProviderID(err.Provider))
}
