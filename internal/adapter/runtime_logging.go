package adapter

import (
	"strings"
	"sync/atomic"

	"goodkind.io/clyde/internal/config"
)

// RuntimeLogging exposes daemon-owned logging knobs that can change without
// reconstructing the adapter server.
type RuntimeLogging struct {
	body atomic.Value
}

// NewRuntimeLogging returns a runtime logging holder seeded from cfg.
func NewRuntimeLogging(cfg config.LoggingConfig) *RuntimeLogging {
	r := &RuntimeLogging{}
	r.Set(cfg)
	return r
}

// Set publishes the current runtime logging settings.
func (r *RuntimeLogging) Set(cfg config.LoggingConfig) {
	if r == nil {
		return
	}
	r.body.Store(normalizeRuntimeLoggingBody(cfg.Body))
}

// Body returns the current body logging settings.
func (r *RuntimeLogging) Body() config.LoggingBody {
	if r == nil {
		return normalizeRuntimeLoggingBody(config.LoggingBody{})
	}
	if v := r.body.Load(); v != nil {
		if body, ok := v.(config.LoggingBody); ok {
			return body
		}
	}
	return normalizeRuntimeLoggingBody(config.LoggingBody{})
}

func normalizeRuntimeLoggingBody(body config.LoggingBody) config.LoggingBody {
	body.Mode = strings.ToLower(strings.TrimSpace(body.Mode))
	if body.Mode == "" {
		body.Mode = "summary"
	}
	if body.MaxKB <= 0 {
		body.MaxKB = 32
	}
	return body
}
