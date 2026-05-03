package contextusage

import (
	"context"
	"fmt"
	"time"

	"goodkind.io/clyde/internal/compact"
)

// countBackend wraps compact.TokenCounter for Layer.Count. Token
// counts for synthetic payloads go through Anthropic's count_tokens
// endpoint; there is no probe equivalent for an in-memory candidate
// because Claude does not know about it yet.
type countBackend struct {
	sessionID    string
	defaultModel string
	apiKey       string

	// builder is injected so tests can substitute a deterministic
	// counter without hitting the network.
	builder func(apiKey, model string) *compact.TokenCounter
}

// newCountBackend returns a backend that constructs a fresh
// TokenCounter per call so the caller-supplied model (via CountOptions)
// can override the default. Errors surface as-is from TokenCounter.
func newCountBackend(sessionID, defaultModel, apiKey string) *countBackend {
	return &countBackend{
		sessionID:    sessionID,
		defaultModel: defaultModel,
		apiKey:       apiKey,
		builder:      compact.NewTokenCounter,
	}
}

// Count invokes Anthropic's count_tokens for a single synthetic user
// message. Returns the raw input_tokens the API reports. Missing API
// key or missing model surface as TokenCounter's native errors; the
// layer passes them up unchanged.
func (c *countBackend) Count(ctx context.Context, content []compact.OutputBlock, model string) (int, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = c.defaultModel
	}
	if effectiveModel == "" {
		return 0, fmt.Errorf("contextusage.count: no model resolved (layer default empty and CountOptions.Model empty)")
	}
	if c.apiKey == "" {
		return 0, fmt.Errorf("contextusage.count: no api key configured for count_tokens")
	}

	counter := c.builder(c.apiKey, effectiveModel)
	started := currentTime()
	sessionContextLog.Logger().DebugContext(ctx, "session.context.count.started",
		"component", "contextusage",
		"subcomponent", "count",
		"session_id", c.sessionID,
		"model", effectiveModel,
		"content_blocks", len(content),
	)
	tokens, err := counter.CountSyntheticUser(ctx, content)
	duration := time.Since(started)
	if err != nil {
		sessionContextLog.Logger().WarnContext(ctx, "session.context.count.failed",
			"component", "contextusage",
			"subcomponent", "count",
			"session_id", c.sessionID,
			"model", effectiveModel,
			"duration_ms", duration.Milliseconds(),
			"err", err,
		)
		return 0, err
	}
	sessionContextLog.Logger().InfoContext(ctx, "session.context.count.completed",
		"component", "contextusage",
		"subcomponent", "count",
		"session_id", c.sessionID,
		"model", effectiveModel,
		"tokens_in", tokens,
		"content_blocks", len(content),
		"duration_ms", duration.Milliseconds(),
	)
	return tokens, nil
}
