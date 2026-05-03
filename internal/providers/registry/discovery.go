// Package registry resolves provider-specific session runtimes.
package registry

import (
	claudediscovery "goodkind.io/clyde/internal/providers/claude/discovery"
	codexdiscovery "goodkind.io/clyde/internal/providers/codex/discovery"
	"goodkind.io/clyde/internal/session"
)

// RegisterDefaultDiscoveryScanners installs the built-in provider discovery
// scanners into the session registry. Provider-aware binaries and tests call
// this explicitly before using session discovery or transparent adoption.
func RegisterDefaultDiscoveryScanners() {
	session.RegisterDiscoveryScanner(session.ProviderClaude, claudediscovery.NewScanner(""))
	session.RegisterDiscoveryScanner(session.ProviderCodex, codexdiscovery.NewScanner(""))
}
