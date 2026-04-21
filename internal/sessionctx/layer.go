package sessionctx

import (
	"context"
	"time"

	"goodkind.io/clyde/internal/compact"
)

// Source tags where a Usage value came from so consumers (loggers,
// tests) can tell a fresh probe result apart from a cache hit.
type Source string

// Source constants cover the three places Usage can originate. A
// caller that wants to force freshness passes UsageOptions.Refresh to
// suppress both cache tiers.
const (
	// SourceProbe is a fresh spawn of claude with get_context_usage.
	SourceProbe Source = "probe"
	// SourceCacheMem is a hit in the in-process sync.Map cache.
	SourceCacheMem Source = "cache_mem"
	// SourceCacheDisk is a hit in the on-disk context.json cache.
	SourceCacheDisk Source = "cache_disk"
)

// Category mirrors Claude Code's /context row. We reuse the type from
// compact so the ContextUsage payload decodes into the shape without
// translation.
type Category = compact.ContextCategory

// Usage is the authoritative answer to "what does /context show for
// this session right now." The embedded ContextUsage carries the
// shape Claude emits. CapturedAt and Source are layer metadata so
// loggers can tell a 30s-old cache hit from a just-spawned probe.
type Usage struct {
	compact.ContextUsage

	CapturedAt time.Time `json:"captured_at"`
	Source     Source    `json:"source"`
}

// StaticOverhead sums everything that behaves as a per-session
// constant: system prompt, tools (including deferred), memory, skills,
// custom agents. Excludes Messages (the trim-able tail), Compact
// buffer (matches the --reserved knob in the planner), and Free space
// (visualization padding, not real tokens).
func (u Usage) StaticOverhead() int {
	return compact.StaticOverheadFromUsage(u.ContextUsage)
}

// TailTokens returns the tokens attributable to post-boundary
// messages, as Claude reports them. This is what compact trims.
func (u Usage) TailTokens() int {
	return u.CategoryTokens("Messages")
}

// CategoryTokens returns the token count for the named category, or
// zero when the category is absent from the response. The match is
// exact; Claude's stable names are surfaced as-is ("System prompt",
// "System tools", "MCP tools", "Memory files", "Skills", "Messages",
// "Compact buffer", "Free space", plus deferred variants suffixed
// " (deferred)").
func (u Usage) CategoryTokens(name string) int {
	total := 0
	for _, cat := range u.Categories {
		if cat.Name == name {
			total += cat.Tokens
		}
	}
	return total
}

// UsageOptions controls a single Layer.Usage call. Zero values
// produce cache-preferred behavior with the layer's default TTL.
type UsageOptions struct {
	// Refresh forces a fresh probe and busts both cache tiers. Use for
	// calibration, where an outdated static_overhead would persist.
	Refresh bool

	// MaxAge caps acceptable cache age. A zero value accepts any hit
	// within the layer's configured TTL. Stricter MaxAge lets callers
	// who care about freshness (TUI context meter) narrow the window
	// without opting into a full refresh.
	MaxAge time.Duration
}

// CountOptions controls a single Layer.Count call. Zero values use
// the layer's configured default model.
type CountOptions struct {
	// Model overrides the layer's default model for this call. Used
	// by callers that count payloads targeted at a specific model and
	// want the honest count for that tokenizer.
	Model string
}

// Layer is the one entry point every session-context caller uses. The
// two methods answer orthogonal questions: Usage is session-wide and
// matches /context exactly; Count is payload-sized and routes to
// Anthropic's count_tokens endpoint.
type Layer interface {
	// Usage returns the live Claude /context for the session. The
	// probe backend is the truth source. Cache tiers serve previous
	// probe results when they satisfy opts.MaxAge and the transcript
	// has not been written since capture. The returned value is by
	// construction identical to what /context prints inside the chat.
	Usage(ctx context.Context, opts UsageOptions) (Usage, error)

	// Count returns Anthropic's count_tokens for a synthetic user
	// message whose content is the supplied block array. Used by the
	// planner target loop, where the payload is a compaction
	// candidate that does not exist on disk yet.
	Count(ctx context.Context, content []compact.OutputBlock, opts CountOptions) (int, error)

	// SessionID returns the UUID this layer is bound to. Callers use
	// it for log correlation when they already hold the layer but
	// not the session reference.
	SessionID() string
}
