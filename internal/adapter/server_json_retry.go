package adapter

import (
	"context"
	"log/slog"
	"strings"
)

// structuredOutputParseFailedEvent is the slog message for first-pass JSON failure.
const structuredOutputParseFailedEvent = "structured-output parse failed; retrying"

// structuredOutputShuntParseFailedEvent is the slog message for shunt first-pass failure.
const structuredOutputShuntParseFailedEvent = "shunt structured-output parse failed; retrying"

// structuredOutputFirstPass runs CoerceJSON and reports whether the result looks like JSON.
func structuredOutputFirstPass(text string) (coerced string, looksLike bool) {
	coerced = CoerceJSON(text)
	return coerced, LooksLikeJSON(coerced)
}

// legacyCollectApplyStructuredOutput runs JSON coercion and optional one-shot retry
// for the legacy `claude -p` collect path. It mutates usage when retry succeeds.
func (s *Server) legacyCollectApplyStructuredOutput(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	text string,
	usage Usage,
	jsonSpec JSONResponseSpec,
	reqID string,
) (finalText string, outUsage Usage, jsonRetried bool) {
	finalText = text
	outUsage = usage
	if jsonSpec.Mode == "" {
		return finalText, outUsage, false
	}
	coerced, ok := structuredOutputFirstPass(text)
	if ok {
		return coerced, outUsage, false
	}
	jsonRetried = true
	s.log.LogAttrs(ctx, slog.LevelWarn, structuredOutputParseFailedEvent,
		slog.String("request_id", reqID),
		slog.Int("first_attempt_bytes", len(text)),
	)
	retrySystem, retryPrompt := BuildPrompt(req.Messages)
	retrySystem = strings.TrimSpace(retrySystem + "\n\n" + jsonSpec.SystemPrompt(true))
	retryRunner := NewRunner(s.deps, model, "", retrySystem, retryPrompt, reqID+"-r")
	retryStdout, retryCancel, spawnErr := retryRunner.Spawn(BackgroundDetachContext())
	if spawnErr != nil {
		return finalText, outUsage, jsonRetried
	}
	defer retryCancel()
	retryText, retryUsage, collectErr := CollectStream(retryStdout)
	if collectErr != nil {
		return finalText, outUsage, jsonRetried
	}
	retryCoerced, retryOK := structuredOutputFirstPass(retryText)
	if retryOK {
		return retryCoerced, Usage{
			PromptTokens:     usage.PromptTokens + retryUsage.PromptTokens,
			CompletionTokens: usage.CompletionTokens + retryUsage.CompletionTokens,
			TotalTokens:      usage.TotalTokens + retryUsage.TotalTokens,
		}, jsonRetried
	}
	return retryText, outUsage, jsonRetried
}

// shuntStructuredOutputNeedsRetry reports whether the shunt path should issue a
// second upstream request after the first HTTP 200 body failed JSON coercion.
func shuntStructuredOutputNeedsRetry(jsonMode string, statusOK bool, coercionOK bool) bool {
	return jsonMode != "" && statusOK && !coercionOK
}
