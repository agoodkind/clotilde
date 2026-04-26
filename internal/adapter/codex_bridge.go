package adapter

import (
	"context"
	"net/http"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

func (s *Server) AppFallbackEnabled() bool {
	return s.cfg.Codex.AppFallback
}

func (s *Server) RunCodexDirect(ctx context.Context, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, emit func(adapteropenai.StreamChunk) error) (any, error) {
	return s.runCodexDirect(ctx, req, model, effort, reqID, emit)
}

func (s *Server) RunCodexManaged(ctx context.Context, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, emit func(adapteropenai.StreamChunk) error) (any, string, bool, error) {
	return s.runCodexManaged(ctx, req, model, effort, reqID, emit)
}

func (s *Server) RunCodexAppFallback(ctx context.Context, req adapteropenai.ChatRequest, reqID string, emit func(adapteropenai.StreamChunk) error) (any, error) {
	return s.runCodexAppFallback(ctx, req, reqID, emit)
}

func (s *Server) EmitRequestStarted(ctx context.Context, model adaptermodel.ResolvedModel, route, reqID, modelID string, stream bool) {
	s.emitRequestStarted(ctx, model, route, reqID, modelID, stream)
}

func (s *Server) EmitRequestStreamOpened(ctx context.Context, model adaptermodel.ResolvedModel, route, reqID, modelID string, stream bool) {
	s.emitRequestStreamOpened(ctx, model, route, reqID, modelID, stream)
}

func (s *Server) NewSSEWriter(w http.ResponseWriter) (adaptercodex.SSEWriter, error) {
	return newSSEWriter(w)
}

func (s *Server) StreamChunkFromTooltrans(ch adapteropenai.StreamChunk) adapteropenai.StreamChunk {
	return streamChunkFromTooltrans(ch)
}

func (s *Server) MergeChunks(reqID, alias string, chunks []adapteropenai.StreamChunk, res any) any {
	typed := res.(codexRunResult)
	return adaptercodex.MergeChunks(reqID, alias, systemFingerprint, chunks, typed)
}

func (s *Server) WriteJSON(w http.ResponseWriter, status int, v any) {
	writeJSON(w, status, v)
}

func (s *Server) LogTerminal(ctx context.Context, ev adapterruntime.RequestEvent) {
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, ev)
}

func (s *Server) SystemFingerprint() string {
	return systemFingerprint
}

func (s *Server) ResultUsage(res any) *adapteropenai.Usage {
	typed := res.(codexRunResult)
	return &typed.Usage
}

func (s *Server) ResultFinishReason(res any) string {
	return res.(codexRunResult).FinishReason
}

func (s *Server) ResultReasoning(res any) (bool, bool) {
	typed := res.(codexRunResult)
	return typed.ReasoningSignaled, typed.ReasoningVisible
}

func (s *Server) ResultDerivedCacheCreationTokens(res any) int {
	return res.(codexRunResult).DerivedCacheCreationTokens
}
