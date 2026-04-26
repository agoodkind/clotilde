package anthropicbackend

import (
	"net/http"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

// EmitActionableStreamError emits a single assistant-shaped chunk that
// describes the upstream failure in user-friendly terms when the SSE
// headers have already been committed. It is the legacy fallback for
// untyped errors; typed *anthropic.UpstreamError values surface a
// native OpenAI error envelope instead (see EmitStreamError on the
// shared SSEWriter).
func EmitActionableStreamError(emit func(adapteropenai.StreamChunk) error, reqID, modelAlias string, err error) error {
	return emit(adapteropenai.StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   modelAlias,
		Choices: []adapteropenai.StreamChoice{{
			Index: 0,
			Delta: adapteropenai.StreamDelta{
				Role:    "assistant",
				Content: ActionableStreamErrorMessage(err),
			},
		}},
	})
}

// ActionableStreamErrorMessage returns the user-facing assistant text
// emitted when an upstream stream fails after SSE headers have already
// been committed (no native error shape is possible at that point).
//
// The function prefers the typed *anthropic.UpstreamError class when
// available, since it carries the actual HTTP status: status 401/403
// implies an auth retry instruction, and a real ResponseClassRetryable
// error with status 429 implies the rate-limit retry instruction.
//
// String matching on the error message is reserved for non-typed
// errors (subprocess output, generic network failures). This avoids
// the regression where any error whose message happened to contain
// the word "rate limit" was rewritten as a misleading "upstream rate
// limit" message even when the actual upstream returned a non-429
// failure.
func ActionableStreamErrorMessage(err error) string {
	if ue, ok := anthropic.AsUpstreamError(err); ok {
		switch ue.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry."
		case http.StatusTooManyRequests:
			return "Clyde adapter hit an upstream rate limit. Wait a moment and retry."
		default:
			return "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry."
		}
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	switch {
	case strings.Contains(msg, "oauth"),
		strings.Contains(msg, "login"),
		strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "forbidden"),
		strings.Contains(msg, "401"):
		return "Clyde adapter upstream auth failed. Re-authenticate Claude with `claude /login`, then retry."
	default:
		return "Clyde adapter request failed upstream. Check ~/.local/state/clyde/clyde.jsonl, then retry."
	}
}
