package adapter

import (
	"context"
	"fmt"
	"net/http"
	"time"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
)

// handleFallback fulfils a chat completion via the local `claude`
// CLI in `-p --output-format stream-json` mode. It is the third
// dispatch leg, gated by [adapter.fallback].
//
// When escalate is true (the on_oauth_failure / both triggers fired
// after an OAuth error), the function returns a non-nil error
// without writing the response on transport-level failures so the
// dispatcher can decide which error surfaces to the client per
// FailureEscalation. When escalate is false (explicit dispatch),
// errors are written to w directly.
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, reqID string, escalate bool) error {
	return anthropicbackend.HandleFallback(s, w, r, req, model, reqID, escalate)
}

// acquireFallback waits on the fallback's own concurrency semaphore.
func (s *Server) acquireFallback(ctx context.Context) error {
	select {
	case s.fbSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for fallback concurrency slot")
	}
}

func (s *Server) releaseFallback() {
	select {
	case <-s.fbSem:
	default:
	}
}
