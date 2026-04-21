package session

import (
	"log/slog"
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

// discoveryScanner is the scan function the cache calls when it needs
// to refresh. Injected so tests can substitute a deterministic fixture.
type discoveryScanner func(projectsDir string) ([]DiscoveryResult, error)

// discoveryCache memoizes the result of ScanProjects so tier-4 resolve
// misses do not re-walk ~/.claude/projects on every call. The cache is
// per-FileStore, keyed implicitly by projectsDir. Access is serialized
// with a mutex; refreshes block concurrent callers rather than racing
// multiple scans, which would multiply the disk cost during a burst.
type discoveryCache struct {
	mu          sync.Mutex
	projectsDir string
	scan        discoveryScanner
	ttl         time.Duration
	results     []DiscoveryResult
	loaded      time.Time
}

// newDiscoveryCache constructs a cache bound to a specific Claude Code
// projects directory. A zero TTL falls back to the package default so
// callers can elect the standard behavior without naming the constant.
// A nil scanner falls back to ScanProjects, which is the production
// path; tests override with a fixture builder.
func newDiscoveryCache(projectsDir string, scan discoveryScanner, ttl time.Duration) *discoveryCache {
	if scan == nil {
		scan = ScanProjects
	}
	if ttl <= 0 {
		ttl = defaultDiscoveryCacheTTL
	}
	return &discoveryCache{
		projectsDir: projectsDir,
		scan:        scan,
		ttl:         ttl,
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
		slog.Debug("session.resolve.cache_hit",
			"component", "session",
			"subcomponent", "resolve_cache",
			"projects_dir", c.projectsDir,
			"results", len(c.results),
			"age_ms", time.Since(c.loaded).Milliseconds(),
			"ttl_ms", c.ttl.Milliseconds(),
		)
		return c.results, nil
	}

	started := time.Now()
	results, err := c.scan(c.projectsDir)
	elapsedMs := time.Since(started).Milliseconds()
	if err != nil {
		slog.Warn("session.resolve.cache_refresh_failed",
			"component", "session",
			"subcomponent", "resolve_cache",
			"projects_dir", c.projectsDir,
			"duration_ms", elapsedMs,
			slog.Any("err", err),
		)
		c.results = nil
		c.loaded = time.Time{}
		return nil, err
	}
	c.results = results
	c.loaded = time.Now()
	slog.Debug("session.resolve.cache_refresh",
		"component", "session",
		"subcomponent", "resolve_cache",
		"projects_dir", c.projectsDir,
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
	slog.Debug("session.resolve.cache_invalidated",
		"component", "session",
		"subcomponent", "resolve_cache",
		"projects_dir", c.projectsDir,
	)
}
