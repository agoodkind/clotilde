package codex

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

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
		res, wsErr := RunWebsocketTransport(ctx, WebsocketTransportConfig{
			URL:            cfg.WebsocketURL,
			Token:          cfg.Token,
			AccountID:      cfg.AccountID,
			RequestID:      cfg.RequestID,
			Alias:          model.Alias,
			ConversationID: conversationID,
			TurnState:      turnState,
			Prewarm:        strings.TrimSpace(wsReq.PreviousResponseID) == "",
			BodyLog:        cfg.BodyLog,
		}, wsReq, emit)
		if wsErr == nil {
			if cfg.Continuation != nil {
				cfg.Continuation.Complete(continuation, fullWSReq, res)
			}
			return res, nil
		}
		if cfg.Continuation != nil {
			cfg.Continuation.Forget(continuation.Key)
		}
		if !errors.Is(wsErr, ErrWebsocketFallbackToHTTP) {
			return NewRunResult("stop"), wsErr
		}
	}
	return RunHTTPTransport(ctx, cfg.HTTPClient, HTTPTransportConfig{
		BaseURL:        cfg.BaseURL,
		Token:          cfg.Token,
		AccountID:      cfg.AccountID,
		RequestID:      cfg.RequestID,
		Alias:          model.Alias,
		ConversationID: strings.TrimSpace(transportPayload.PromptCache),
		BodyLog:        cfg.BodyLog,
	}, transportPayload, emit)
}
