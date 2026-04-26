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

func (s *Server) Log() *slog.Logger {
	return s.log
}

func (s *Server) NewAnthropicSSEWriter(w http.ResponseWriter) (anthropicbackend.ResponseSSEWriter, error) {
	return newSSEWriter(w)
}

func (s *Server) AnthropicStreamClient() anthropicbackend.StreamClient {
	return s.anthr
}

func (s *Server) StreamChunkHasVisibleContent(chunk adapteropenai.StreamChunk) bool {
	return streamChunkHasVisibleContent(chunk)
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

func (s *Server) NoticesEnabled() bool {
	return s.cfg.Notices.EnabledOrDefault()
}

func (s *Server) ClaimNotice(kind string, resetsAt time.Time) bool {
	return Claim(kind, resetsAt)
}

func (s *Server) LogCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, u anthropic.Usage) {
	s.logCacheUsageAnthropic(ctx, backend, reqID, alias, u)
}

func (s *Server) FallbackClient() anthropicbackend.FallbackClient {
	return s.fb
}

func (s *Server) AcquireFallback(ctx context.Context) error {
	return s.acquireFallback(ctx)
}

func (s *Server) ReleaseFallback() {
	s.releaseFallback()
}

func (s *Server) FallbackJSONSystemPrompt(jsonSpec any) string {
	spec, ok := jsonSpec.(JSONResponseSpec)
	if !ok {
		return ""
	}
	return spec.SystemPrompt(false)
}

func (s *Server) FallbackStreamPassthrough() bool {
	return s.cfg.Fallback.StreamPassthrough
}

func (s *Server) FallbackDropUnsupported() bool {
	return s.cfg.Fallback.DropUnsupported
}

func (s *Server) FallbackTranscriptSynthesisEnabled() bool {
	return s.cfg.Fallback.TranscriptSynthesisEnabled
}

func (s *Server) FallbackTranscriptWorkspaceDir(alias string) string {
	return s.cfg.Fallback.ResolveTranscriptWorkspaceDir(alias)
}

func (s *Server) LogCacheUsageFallback(ctx context.Context, backend, reqID, alias string, promptTokens, cacheCreationTokens, cacheReadTokens int) {
	s.logCacheUsage(ctx, backend, reqID, alias, promptTokens, cacheCreationTokens, cacheReadTokens)
}

func (s *Server) UnclaimNotice(kind string, resetsAt time.Time) {
	Unclaim(kind, resetsAt)
}
