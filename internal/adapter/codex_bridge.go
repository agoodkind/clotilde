package adapter

import (
	"context"
	"net/http"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// Methods that the new provider-based Codex dispatch path needs from
// the Server. The legacy app-server / managed / fallback methods are
// gone; the websocket Provider implementation reaches the same
// behavior directly without bouncing through this bridge.

func (s *Server) EmitRequestStarted(ctx context.Context, model adaptermodel.ResolvedModel, route, reqID, modelID string, stream bool) {
	s.emitRequestStarted(ctx, model, route, reqID, modelID, stream)
}

func (s *Server) EmitRequestStreamOpened(ctx context.Context, model adaptermodel.ResolvedModel, route, reqID, modelID string, stream bool) {
	s.emitRequestStreamOpened(ctx, model, route, reqID, modelID, stream)
}

func (s *Server) NewSSEWriter(w http.ResponseWriter) (adaptercodex.SSEWriter, error) {
	return newSSEWriter(w)
}

// StreamChunkFromTooltrans is a deep-copy reformer that anthropic's
// dispatcher contract still depends on. Codex no longer needs it
// because the new provider path forwards chunks directly through the
// SSE writer.
func (s *Server) StreamChunkFromTooltrans(ch adapteropenai.StreamChunk) adapteropenai.StreamChunk {
	return streamChunkFromTooltrans(ch)
}

func (s *Server) MergeChunks(reqID, alias string, chunks []adapteropenai.StreamChunk, res adaptercodex.RunResult) adapteropenai.ChatResponse {
	return adaptercodex.MergeChunks(reqID, alias, systemFingerprint, chunks, res)
}

func (s *Server) WriteJSON(w http.ResponseWriter, status int, v adapteropenai.ChatResponse) {
	writeJSON(w, status, v)
}

func (s *Server) LogTerminal(ctx context.Context, ev adapterruntime.RequestEvent) {
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, ev)
}

func (s *Server) SystemFingerprint() string {
	return systemFingerprint
}
