package sessionctx

import (
	"context"
	"log/slog"
	"time"

	"goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/session"
)

// defaultLayer composes the in-memory cache, the on-disk cache, and
// the probe backend for Usage, and wraps the count backend for Count.
// It is the production implementation of Layer; tests substitute an
// alternative that implements the interface directly.
type defaultLayer struct {
	sessionID      string
	transcriptPath string
	workDir        string
	defaultModel   string

	memCache   *memoryCache
	diskCache  *diskCache
	probe      *probeBackend
	countCalls *countBackend
}

// NewDefault builds a Layer bound to one session. Callers pass the
// session metadata (for UUID, transcript path, work dir), the planner
// model default, and an Anthropic API key. The API key is only used
// by Count; Usage never needs it because the probe carries the live
// session's own auth.
func NewDefault(sess *session.Session, model, apiKey string) Layer {
	if sess == nil {
		return &nullLayer{}
	}
	return &defaultLayer{
		sessionID:      sess.Metadata.SessionID,
		transcriptPath: sess.Metadata.TranscriptPath,
		workDir:        sess.Metadata.WorkDir,
		defaultModel:   model,
		memCache:       newMemoryCache(),
		diskCache:      newDiskCache(sess.Metadata.SessionID, sess.Metadata.TranscriptPath, config.DefaultStateDir()),
		probe:          newProbeBackend(sess.Metadata.SessionID, sess.Metadata.WorkDir),
		countCalls:     newCountBackend(sess.Metadata.SessionID, model, apiKey),
	}
}

// SessionID returns the UUID this layer is bound to so loggers can
// correlate across call sites without re-threading the session.
func (l *defaultLayer) SessionID() string { return l.sessionID }

// Usage walks the cache hierarchy (memory, disk, probe), returning the
// first hit that satisfies opts. A successful probe populates both
// cache tiers before returning.
func (l *defaultLayer) Usage(ctx context.Context, opts UsageOptions) (Usage, error) {
	if opts.Refresh {
		l.memCache.invalidate()
		l.diskCache.invalidate()
		slog.Debug("session.context.usage.cache_miss",
			"component", "sessionctx",
			"subcomponent", "usage",
			"session_id", l.sessionID,
			"reason", "refresh_requested",
		)
	}

	if !opts.Refresh {
		if hit := l.memCache.get(l.transcriptPath, opts); hit != nil {
			slog.Debug("session.context.usage.cache_hit",
				"component", "sessionctx",
				"subcomponent", "usage",
				"session_id", l.sessionID,
				"source", string(SourceCacheMem),
				"age_ms", time.Since(hit.CapturedAt).Milliseconds(),
			)
			return Usage{
				ContextUsage: hit.Usage,
				CapturedAt:   hit.CapturedAt,
				Source:       SourceCacheMem,
			}, nil
		}

		if hit, err := l.diskCache.read(opts); err != nil {
			slog.Warn("session.context.disk_cache.read_failed",
				"component", "sessionctx",
				"subcomponent", "disk_cache",
				"session_id", l.sessionID,
				"err", err,
			)
		} else if hit != nil {
			slog.Debug("session.context.usage.cache_hit",
				"component", "sessionctx",
				"subcomponent", "usage",
				"session_id", l.sessionID,
				"source", string(SourceCacheDisk),
				"age_ms", time.Since(hit.CapturedAt).Milliseconds(),
			)
			// Populate the in-memory tier so subsequent calls in this
			// process do not re-read disk.
			l.memCache.put(hit)
			return Usage{
				ContextUsage: hit.Usage,
				CapturedAt:   hit.CapturedAt,
				Source:       SourceCacheDisk,
			}, nil
		}
	}

	slog.Debug("session.context.usage.cache_miss",
		"component", "sessionctx",
		"subcomponent", "usage",
		"session_id", l.sessionID,
		"reason", reasonForMiss(opts),
	)

	raw, err := l.probe.Fetch(ctx)
	if err != nil {
		return Usage{}, err
	}
	captured := time.Now().UTC()
	payload := &cachedUsage{
		SchemaVersion:   diskCacheSchemaV,
		Usage:           raw,
		CapturedAt:      captured,
		TranscriptMTime: transcriptMTimeNs(l.transcriptPath),
		TranscriptPath:  l.transcriptPath,
	}
	l.memCache.put(payload)
	l.diskCache.write(payload)
	return Usage{
		ContextUsage: raw,
		CapturedAt:   captured,
		Source:       SourceProbe,
	}, nil
}

// Count routes through the count backend without touching the cache,
// because each candidate payload the planner measures is unique and
// caching would mean comparing full content arrays.
func (l *defaultLayer) Count(ctx context.Context, content []compact.OutputBlock, opts CountOptions) (int, error) {
	return l.countCalls.Count(ctx, content, opts.Model)
}

// reasonForMiss classifies the cache-miss cause for the structured
// log. Callers infer from opts because the miss branch does not know
// internally why both tiers returned nil.
func reasonForMiss(opts UsageOptions) string {
	if opts.Refresh {
		return "refresh_requested"
	}
	if opts.MaxAge > 0 {
		return "absent_or_ttl_or_maxage_or_mtime"
	}
	return "absent_or_ttl_or_mtime"
}

// nullLayer is returned by NewDefault when the session is nil. Every
// call fails with a stable error so callers can test for this without
// nil-checking the Layer value itself.
type nullLayer struct {
	Unavailable bool
}

func (n *nullLayer) Usage(ctx context.Context, opts UsageOptions) (Usage, error) {
	return Usage{}, errNullLayer
}

func (n *nullLayer) Count(ctx context.Context, content []compact.OutputBlock, opts CountOptions) (int, error) {
	return 0, errNullLayer
}

func (n *nullLayer) SessionID() string { return "" }
