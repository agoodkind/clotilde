package session

import (
	"strings"
	"sync"
	"time"
)

// defaultDiscoveryCacheTTL bounds how stale tier-4 resolve scans are
// allowed to get. The daemon's scheduled scanner (daemon/server.go) runs
// every five minutes and is authoritative for background adoption. The
// tier-4 cache exists solely to amortize the cost of CLI invocations
// that hit the resolver within a short window of each other. Sixty
// seconds is long enough that repeated compact or resume calls during a
// single work session share one walk, and short enough that a newly
// named Claude Code chat is discoverable almost immediately.
const defaultDiscoveryCacheTTL = 60 * time.Second

// discoveryCache memoizes the result of provider scanners so tier-4
// resolve misses do not re-walk provider transcript roots on every
// call. Access is serialized
// with a mutex; refreshes block concurrent callers rather than racing
// multiple scans, which would multiply the disk cost during a burst.
type discoveryCache struct {
	mu       sync.Mutex
	scanners []DiscoveryScanner
	ttl      time.Duration
	results  []DiscoveryResult
	loaded   time.Time
}

func newDiscoveryCache(scanners []DiscoveryScanner, ttl time.Duration) *discoveryCache {
	if len(scanners) == 0 {
		return nil
	}
	if ttl <= 0 {
		ttl = defaultDiscoveryCacheTTL
	}
	return &discoveryCache{
		scanners: scanners,
		ttl:      ttl,
	}
}

// Get returns the latest scan results, refreshing from disk when the
// cache is empty or stale. Callers receive a snapshot and must not
// mutate the slice. Scan errors fall through to the caller; the cache
// deliberately does not retain stale results after a failed refresh
// because a repeated failure would otherwise mask surfacing the error.
func (c *discoveryCache) Get() ([]DiscoveryResult, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.loaded.IsZero() && time.Since(c.loaded) < c.ttl {
		sessionResolveLog.Logger().Debug("session.resolve.cache_hit",
			"component", "session",
			"subcomponent", "resolve_cache",
			"providers", c.providerNames(),
			"results", len(c.results),
			"age_ms", time.Since(c.loaded).Milliseconds(),
			"ttl_ms", c.ttl.Milliseconds(),
		)
		return c.results, nil
	}

	started := currentTime()
	results, err := c.scanAll()
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		sessionResolveLog.Logger().Warn("session.resolve.cache_refresh_failed",
			"component", "session",
			"subcomponent", "resolve_cache",
			"providers", c.providerNames(),
			"duration_ms", elapsedMs,
			"err", err,
		)
		c.results = nil
		c.loaded = time.Time{}
		return nil, err
	}
	c.results = results
	c.loaded = currentTime()
	sessionResolveLog.Logger().Debug("session.resolve.cache_refresh",
		"component", "session",
		"subcomponent", "resolve_cache",
		"providers", c.providerNames(),
		"results", len(results),
		"duration_ms", elapsedMs,
	)
	return results, nil
}

// Invalidate zeroes the load timestamp so the next Get triggers a
// scan. Called after a successful adoption to make sure the newly
// adopted transcript does not continue to appear as an "unknown"
// candidate for the rest of the TTL window.
func (c *discoveryCache) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.loaded = time.Time{}
	sessionResolveLog.Logger().Debug("session.resolve.cache_invalidated",
		"component", "session",
		"subcomponent", "resolve_cache",
		"providers", c.providerNames(),
	)
}

func (c *discoveryCache) scanAll() ([]DiscoveryResult, error) {
	results := make([]DiscoveryResult, 0)
	for _, scanner := range c.scanners {
		scanned, err := scanner.Scan()
		if err != nil {
			return nil, err
		}
		results = append(results, scanned...)
	}
	return results, nil
}

func (c *discoveryCache) providerNames() string {
	names := make([]string, 0, len(c.scanners))
	for _, scanner := range c.scanners {
		names = append(names, string(scanner.Provider()))
	}
	return strings.Join(names, ",")
}
