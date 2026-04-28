package codex

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type DirectConfig struct {
	HTTPClient       *http.Client
	BaseURL          string
	WebsocketEnabled bool
	WebsocketURL     string
	Token            string
	AccountID        string
	RequestID        string
	Continuation     *ContinuationStore
	Log              *slog.Logger
	BodyLog          BodyLogConfig
}

func RunDirect(
	ctx context.Context,
	cfg DirectConfig,
	req adapteropenai.ChatRequest,
	model adaptermodel.ResolvedModel,
	effort string,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	transportPayload := BuildRequest(req, model, effort)
	if cfg.WebsocketEnabled {
		conversationID := strings.TrimSpace(transportPayload.PromptCache)
		if conversationID != "" {
			transportPayload.ClientMetadata = ClientMetadata(cfg.AccountID, CodexWindowID(conversationID))
		}
		wsReq := ResponseCreateRequestFromHTTP(transportPayload)
		fullWSReq := wsReq
		turnState := NewTurnState()
		var continuation ContinuationDecision
		if cfg.Continuation != nil {
			continuation = cfg.Continuation.Prepare(fullWSReq)
			rollingRate, rollingWindow := cfg.Continuation.RecordHitRate(continuation.Key, continuation.Hit)
			continuationTelemetry := ContinuationTelemetry{
				RequestID:           cfg.RequestID,
				Alias:               model.Alias,
				Transport:           "responses_websocket",
				Key:                 continuation.Key,
				Hit:                 continuation.Hit,
				MissReason:          continuation.MissReason,
				MismatchField:       continuation.MismatchField,
				FingerprintMatch:    continuation.FingerprintMatch,
				StoredFingerprint:   continuation.StoredFingerprint,
				IncomingFingerprint: continuation.IncomingFingerprint,
				PreviousResponseID:  continuation.PreviousResponseID,
				IncrementalCount:    len(continuation.IncrementalInput),
				ExpectedEventCount:  continuation.Diagnostics.ExpectedEventCount,
				CurrentEventCount:   continuation.Diagnostics.CurrentEventCount,
				BaselineMatchStart:  continuation.Diagnostics.MatchStart,
				BaselineMatchEnd:    continuation.Diagnostics.MatchEnd,
				RollingHitRate:      rollingRate,
				RollingWindowSize:   rollingWindow,
			}
			if mismatch := continuation.Diagnostics.Mismatch; mismatch != nil {
				continuationTelemetry.MismatchExpectedIndex = mismatch.ExpectedEventIndex
				continuationTelemetry.MismatchCurrentIndex = mismatch.CurrentEventIndex
				continuationTelemetry.MismatchExpectedItem = mismatch.ExpectedItemIndex
				continuationTelemetry.MismatchCurrentItem = mismatch.CurrentItemIndex
				continuationTelemetry.MismatchExpected = mismatch.Expected
				continuationTelemetry.MismatchCurrent = mismatch.Current
				continuationTelemetry.MismatchDiffSummary = MismatchDiffSummary(mismatch.Expected, mismatch.Current)
			}
			LogContinuationDecision(ctx, cfg.Log, continuationTelemetry)
			if continuation.Hit {
				wsReq = WithPreviousResponseID(wsReq, continuation.PreviousResponseID, continuation.IncrementalInput)
			}
		}
		wsCfg := WebsocketTransportConfig{
			URL:            cfg.WebsocketURL,
			Token:          cfg.Token,
			AccountID:      cfg.AccountID,
			RequestID:      cfg.RequestID,
			Alias:          model.Alias,
			ConversationID: conversationID,
			TurnState:      turnState,
			Prewarm:        strings.TrimSpace(wsReq.PreviousResponseID) == "",
			BodyLog:        cfg.BodyLog,
		}
		wsAttemptStarted := time.Now()
		res, wsErr := RunWebsocketTransport(ctx, wsCfg, wsReq, emit)
		if wsErr == nil {
			if cfg.Continuation != nil {
				cfg.Continuation.Complete(continuation, fullWSReq, res)
			}
			return res, nil
		}
		// Upstream expired the stored response_id. Drop the stored
		// entry, rebuild without previous_response_id, retry once with
		// the full conversation. Cursor sends the entire conversation
		// every turn anyway, so the retry has all the context the
		// upstream needs to recreate state.
		//
		// Heavy logging on this path. Continuation/cache bugs are hard
		// to track down after the fact, so every transition records
		// enough fields that a single grep on a request_id reconstructs
		// the full sequence.
		if isPreviousResponseNotFound(wsErr) {
			expiredAttemptDuration := time.Since(wsAttemptStarted).Milliseconds()
			incrementalCount := len(continuation.IncrementalInput)
			fullInputCount := len(fullWSReq.Input)
			storeSizeBefore := -1
			if cfg.Continuation != nil {
				storeSizeBefore = cfg.Continuation.Size()
			}
			if cfg.Log != nil {
				cfg.Log.WarnContext(ctx, "adapter.codex.continuation.expired_detected",
					"component", "adapter",
					"subcomponent", "codex",
					"request_id", cfg.RequestID,
					"alias", model.Alias,
					"model", model.ClaudeModel,
					"conversation_id", conversationID,
					"continuation_key", continuation.Key,
					"expired_response_id", continuation.PreviousResponseID,
					"continuation_hit_was_true", continuation.Hit,
					"continuation_age_unknown", true,
					"incremental_input_count", incrementalCount,
					"full_input_count", fullInputCount,
					"first_attempt_duration_ms", expiredAttemptDuration,
					"upstream_error", wsErr.Error(),
					"continuation_store_size_before_forget", storeSizeBefore,
				)
			}
			if cfg.Continuation != nil {
				cfg.Continuation.Forget(continuation.Key)
				if cfg.Log != nil {
					cfg.Log.InfoContext(ctx, "adapter.codex.continuation.expired_forgotten",
						"component", "adapter",
						"subcomponent", "codex",
						"request_id", cfg.RequestID,
						"continuation_key", continuation.Key,
						"continuation_store_size_after_forget", cfg.Continuation.Size(),
					)
				}
			}
			retryReq := fullWSReq
			retryReq.PreviousResponseID = ""
			retryCfg := wsCfg
			retryCfg.Prewarm = true
			retryStarted := time.Now()
			if cfg.Log != nil {
				cfg.Log.InfoContext(ctx, "adapter.codex.continuation.expired_retry_started",
					"component", "adapter",
					"subcomponent", "codex",
					"request_id", cfg.RequestID,
					"alias", model.Alias,
					"conversation_id", conversationID,
					"continuation_key", continuation.Key,
					"retry_input_count", fullInputCount,
					"prewarm", retryCfg.Prewarm,
				)
			}
			res, wsErr = RunWebsocketTransport(ctx, retryCfg, retryReq, emit)
			retryDuration := time.Since(retryStarted).Milliseconds()
			if wsErr == nil {
				if cfg.Continuation != nil {
					cfg.Continuation.Complete(continuation, fullWSReq, res)
				}
				if cfg.Log != nil {
					cfg.Log.InfoContext(ctx, "adapter.codex.continuation.expired_recovered",
						"component", "adapter",
						"subcomponent", "codex",
						"request_id", cfg.RequestID,
						"alias", model.Alias,
						"conversation_id", conversationID,
						"continuation_key", continuation.Key,
						"expired_response_id", continuation.PreviousResponseID,
						"new_response_id", res.ResponseID,
						"first_attempt_duration_ms", expiredAttemptDuration,
						"retry_duration_ms", retryDuration,
						"total_duration_ms", expiredAttemptDuration+retryDuration,
						"prompt_tokens", res.Usage.PromptTokens,
						"completion_tokens", res.Usage.CompletionTokens,
						"cache_read_tokens", res.Usage.CachedTokens(),
					)
				}
				return res, nil
			}
			if cfg.Log != nil {
				cfg.Log.ErrorContext(ctx, "adapter.codex.continuation.expired_retry_failed",
					"component", "adapter",
					"subcomponent", "codex",
					"request_id", cfg.RequestID,
					"alias", model.Alias,
					"conversation_id", conversationID,
					"continuation_key", continuation.Key,
					"expired_response_id", continuation.PreviousResponseID,
					"retry_duration_ms", retryDuration,
					"first_error", "Previous response with id ... not found.",
					"retry_was_full_replay", true,
					"err", wsErr,
				)
			}
		}
		// Do not Forget on other transport errors. The prior response
		// entry is still valid on the upstream thread; clearing it
		// here causes a false `no_prior_response` on the next turn
		// from a sibling request that shares the same conversation
		// key. The next successful Complete overwrites the entry, so
		// stale data cannot persist beyond one good turn.
		return NewRunResult("stop"), wsErr
	}
	// Codex is websocket-only after Plan 5 + Step H. The HTTPS/SSE
	// transport was removed; reaching this point means the websocket
	// was disabled by configuration, which is no longer a supported
	// state. Surface a clear error rather than silently mishandling.
	return NewRunResult("stop"), errCodexWebsocketDisabled
}

var errCodexWebsocketDisabled = errors.New("codex websocket transport is disabled but no HTTPS fallback exists")

// isPreviousResponseNotFound reports whether the upstream error message
// indicates that the supplied previous_response_id has been expired or
// garbage-collected by ChatGPT Pro. The upstream returns a 4xx with a
// human-readable string; we match on the stable substring rather than
// a typed error code because the upstream does not advertise one.
func isPreviousResponseNotFound(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "Previous response with id") &&
		strings.Contains(msg, "not found")
}
