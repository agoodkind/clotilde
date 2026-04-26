package adapter

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

func (s *Server) AnthropicConfigured() bool {
	return s.anthr != nil
}

func (s *Server) AcquirePrimary(ctx any) error {
	return s.acquire(ctx.(context.Context))
}

func (s *Server) ReleasePrimary() {
	s.release()
}

func (s *Server) BuildAnthropicWire(req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort string, jsonSpec any, reqID string) (anthropic.Request, error) {
	return s.buildAnthropicWire(req, model, effort, jsonSpec.(JSONResponseSpec), reqID)
}

func (s *Server) StreamOAuth(w http.ResponseWriter, r *http.Request, req anthropic.Request, model adaptermodel.ResolvedModel, reqID string, started time.Time, escalate bool, includeUsage bool, trackerKey string) error {
	return s.streamOAuth(w, r, req, model, reqID, started, escalate, includeUsage, trackerKey)
}

func (s *Server) CollectOAuth(w http.ResponseWriter, ctx any, req anthropic.Request, model adaptermodel.ResolvedModel, reqID string, started time.Time, jsonSpec any, escalate bool, trackerKey string) error {
	return s.collectOAuth(w, ctx.(context.Context), req, model, reqID, started, jsonSpec.(JSONResponseSpec), escalate, trackerKey)
}

func (s *Server) ParseResponseFormat(raw any) any {
	return ParseResponseFormat(raw.(json.RawMessage))
}

func (s *Server) RequestContextTrackerKey(req adapteropenai.ChatRequest, modelAlias string) string {
	return requestContextTrackerKey(req, modelAlias)
}

func (s *Server) WriteError(w http.ResponseWriter, status int, code, msg string) {
	writeError(w, status, code, msg)
}

func (s *Server) HandleOAuth(w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, escalate bool) error {
	return anthropicbackend.Handle(s, w, r, req, model, effort, reqID, escalate)
}

func (s *Server) HandleFallback(w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, reqID string, escalate bool) error {
	return s.handleFallback(w, r, req, model, reqID, escalate)
}

func (s *Server) HasShunt(name string) bool {
	_, ok := s.registry.Shunt(name)
	return ok
}

func (s *Server) ForwardShunt(w http.ResponseWriter, r *http.Request, model adaptermodel.ResolvedModel, shuntName string, body []byte) {
	model.Backend = adaptermodel.BackendShunt
	model.Shunt = shuntName
	s.forwardShunt(w, r, model, body)
}

func (s *Server) SurfaceFallbackFailure(w http.ResponseWriter, anthErr, fbErr error, failureEscalation string) {
	s.surfaceFallbackFailure(w, anthErr, fbErr, failureEscalation)
}

func (s *Server) Log() *slog.Logger {
	return s.log
}

func (s *Server) NewAnthropicSSEWriter(w http.ResponseWriter) (anthropicbackend.ResponseSSEWriter, error) {
	return newSSEWriter(w)
}

func (s *Server) StreamChunkHasVisibleContent(chunk adapteropenai.StreamChunk) bool {
	return streamChunkHasVisibleContent(chunk)
}

func (s *Server) RunOAuthTranslatorStream(ctx context.Context, req anthropic.Request, model adaptermodel.ResolvedModel, reqID string, emit func(tooltrans.OpenAIStreamChunk) error) (anthropic.Usage, string, string, error) {
	return s.runOAuthTranslatorStream(ctx, req, model, reqID, emit)
}

func (s *Server) TrackAnthropicContextUsage(key string, raw adapteropenai.Usage) anthropicbackend.TrackedUsage {
	tracked := s.ctxUsage.Track(key, Usage(raw))
	return anthropicbackend.TrackedUsage{
		Usage:      adapteropenai.Usage(tracked.usage),
		RawPrompt:  tracked.rawPrompt,
		RawTotal:   tracked.rawTotal,
		RolledFrom: tracked.rolledFrom,
	}
}

// JSONCoercion translates the root-side JSONResponseSpec passed via
// the Dispatcher.ParseResponseFormat hook into the neutral
// JSONCoercion contract the Anthropic merger expects.
func (s *Server) JSONCoercion(jsonSpec any) anthropicbackend.JSONCoercion {
	spec, ok := jsonSpec.(JSONResponseSpec)
	if !ok || spec.Mode == "" {
		return anthropicbackend.JSONCoercion{}
	}
	return anthropicbackend.JSONCoercion{
		Coerce:   CoerceJSON,
		Validate: LooksLikeJSON,
	}
}

func (s *Server) CacheTTL() string {
	return s.cfg.ClientIdentity.PromptCacheTTL
}

func (s *Server) LogCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, u anthropic.Usage) {
	s.logCacheUsageAnthropic(ctx, backend, reqID, alias, u)
}

func (s *Server) UnclaimNotice(notice *anthropic.Notice) {
	if notice == nil {
		return
	}
	Unclaim(notice.Kind, notice.ResetsAt)
}
