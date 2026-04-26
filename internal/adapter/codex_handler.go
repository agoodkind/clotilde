package adapter

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexRunResult = adaptercodex.RunResult

var (
	codexNow       = time.Now
	codexGetwd     = os.Getwd
	codexShellName = func() string {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			return "sh"
		}
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}
)

func init() {
	adaptercodex.NowFunc = func() time.Time { return codexNow() }
	adaptercodex.GetwdFn = func() (string, error) { return codexGetwd() }
	adaptercodex.ShellNameFn = func() string { return codexShellName() }
}

func sanitizeForUpstreamCache(text string) string { return adaptercodex.SanitizeForUpstreamCache(text) }

func codexMapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}

func codexItemType(item map[string]any) string   { return codexMapString(item, "type") }
func codexItemStatus(item map[string]any) string { return codexMapString(item, "status") }

// runCodexDirect builds the Codex transport request through the
// backend-local `adaptercodex.BuildRequest` entrypoint, then dispatches
// it through the websocket-or-HTTP selector that the Codex package
// owns. The root facade keeps only auth/account plumbing.
func (s *Server) runCodexDirect(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, error) {
	token, err := s.readCodexAccessToken()
	if err != nil {
		return adaptercodex.NewRunResult("stop"), err
	}
	transportPayload := adaptercodex.BuildRequest(req, model, effort)
	if s.codexWebsocketEnabled() {
		wsReq := adaptercodex.ResponseCreateRequestFromHTTP(transportPayload)
		fullWSReq := wsReq
		var continuation adaptercodex.ContinuationDecision
		if s.codexContinue != nil {
			continuation = s.codexContinue.Prepare(fullWSReq)
			adaptercodex.LogContinuationDecision(ctx, s.log, adaptercodex.ContinuationTelemetry{
				RequestID:          reqID,
				Alias:              model.Alias,
				Transport:          "responses_websocket",
				Key:                continuation.Key,
				Hit:                continuation.Hit,
				MissReason:         continuation.MissReason,
				FingerprintMatch:   continuation.FingerprintMatch,
				PreviousResponseID: continuation.PreviousResponseID,
				IncrementalCount:   len(continuation.IncrementalInput),
			})
			if continuation.Hit {
				wsReq = adaptercodex.WithPreviousResponseID(wsReq, continuation.PreviousResponseID, continuation.IncrementalInput)
			}
		}
		res, wsErr := adaptercodex.RunWebsocketTransport(ctx, adaptercodex.WebsocketTransportConfig{
			URL:       s.codexWebsocketURL(),
			Token:     token,
			RequestID: reqID,
			Alias:     model.Alias,
		}, wsReq, emit)
		if wsErr == nil {
			if s.codexContinue != nil {
				s.codexContinue.Complete(continuation, fullWSReq, res)
			}
			return res, nil
		}
		if s.codexContinue != nil {
			s.codexContinue.Forget(continuation.Key)
		}
		if !errors.Is(wsErr, adaptercodex.ErrWebsocketFallbackToHTTP) {
			return adaptercodex.NewRunResult("stop"), wsErr
		}
	}
	return adaptercodex.RunHTTPTransport(ctx, s.httpClient, adaptercodex.HTTPTransportConfig{
		BaseURL:        s.codexBaseURL(),
		Token:          token,
		AccountID:      s.readCodexAccountID(),
		RequestID:      reqID,
		Alias:          model.Alias,
		ConversationID: strings.TrimSpace(transportPayload.PromptCache),
	}, transportPayload, emit)
}

func (s *Server) collectCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Collect(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) streamCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string, started time.Time) error {
	return adaptercodex.Stream(s, w, r, req, model, effort, reqID, started)
}

func (s *Server) dispatchCodex(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, effort, reqID string) {
	started := time.Now()
	if req.Stream {
		if err := s.streamCodex(w, r, req, model, effort, reqID, started); err != nil {
			writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
		}
		return
	}
	if err := s.collectCodex(w, r, req, model, effort, reqID, started); err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
	}
}
