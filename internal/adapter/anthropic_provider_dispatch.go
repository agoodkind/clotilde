package adapter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	adapterresolver "goodkind.io/clyde/internal/adapter/resolver"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

func (s *Server) dispatchAnthropicProvider(
	w http.ResponseWriter,
	r *http.Request,
	effort string,
	reqID string,
	resolvedReq adapterresolver.ResolvedRequest,
) error {
	ctx := anthropic.WithRequestID(r.Context(), reqID)
	if resolvedReq.OpenAI.Stream {
		return s.dispatchAnthropicProviderStream(ctx, w, reqID, resolvedReq)
	}
	return s.dispatchAnthropicProviderCollect(ctx, w, effort, resolvedReq)
}

func (s *Server) dispatchAnthropicProviderCollect(
	ctx context.Context,
	w http.ResponseWriter,
	_ string,
	resolvedReq adapterresolver.ResolvedRequest,
) error {
	collector := newProviderCollectorWriter()
	result, runErr := s.anthropicProvider.Execute(ctx, resolvedReq, collector)
	if runErr != nil {
		writeAnthropicProviderError(w, runErr)
		return nil
	}
	if result.FinalResponse == nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "anthropic provider collect path produced no final response")
		return nil
	}
	writeJSON(w, http.StatusOK, *result.FinalResponse)
	return nil
}

func (s *Server) dispatchAnthropicProviderStream(
	ctx context.Context,
	w http.ResponseWriter,
	reqID string,
	resolvedReq adapterresolver.ResolvedRequest,
) error {
	model := anthropicResolvedModelFromRequest(resolvedReq)
	streamWriter, err := newProviderStreamWriter(s, w, reqID, model.Alias, "anthropic")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return nil
	}
	_, runErr := s.anthropicProvider.Execute(ctx, resolvedReq, streamWriter)
	if runErr != nil {
		writeAnthropicProviderError(w, runErr)
		return nil
	}
	return nil
}

func (s *Server) prepareAnthropicProviderRequest(
	ctx context.Context,
	req adapterresolver.ResolvedRequest,
	reqID string,
) (anthropic.PreparedRequest, error) {
	_ = ctx
	model := anthropicResolvedModelFromRequest(req)
	jsonSpec := ParseResponseFormat(req.OpenAI.ResponseFormat)
	anthReq, err := s.buildAnthropicWire(req.OpenAI, model, req.Effort.String(), jsonSpec, reqID)
	if err != nil {
		return anthropic.PreparedRequest{}, &anthropic.ExecuteError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_request",
			Message: err.Error(),
			Cause:   err,
		}
	}
	jsonCoercion := anthropic.JSONCoercion{}
	if jsonSpec.Mode != "" {
		jsonCoercion = anthropic.JSONCoercion{
			Coerce:   CoerceJSON,
			Validate: LooksLikeJSON,
		}
	}
	return anthropic.PreparedRequest{
		Request:      anthReq,
		Model:        model,
		RequestID:    reqID,
		TrackerKey:   requestContextTrackerKey(req.OpenAI, model.Alias),
		JSONCoercion: jsonCoercion,
		IncludeUsage: req.OpenAI.StreamOptions != nil && req.OpenAI.StreamOptions.IncludeUsage,
	}, nil
}

func (s *Server) executeAnthropicPreparedRequest(
	ctx context.Context,
	prepared anthropic.PreparedRequest,
	writer adapterprovider.EventWriter,
) (adapterprovider.Result, error) {
	if s.anthr == nil {
		return adapterprovider.Result{}, &anthropic.ExecuteError{
			Status:  http.StatusInternalServerError,
			Code:    "oauth_unconfigured",
			Message: "adapter built without anthropic client; set adapter.direct_oauth=true and restart",
		}
	}
	if err := s.acquire(ctx); err != nil {
		return adapterprovider.Result{}, &anthropic.ExecuteError{
			Status:  http.StatusTooManyRequests,
			Code:    "rate_limited",
			Message: fmt.Sprint(err),
			Cause:   err,
		}
	}
	defer s.release()
	if prepared.Request.Stream {
		return s.executeAnthropicPreparedStream(ctx, prepared, writer)
	}
	return s.executeAnthropicPreparedCollect(ctx, prepared, writer)
}

func (s *Server) executeAnthropicPreparedCollect(
	ctx context.Context,
	prepared anthropic.PreparedRequest,
	writer adapterprovider.EventWriter,
) (adapterprovider.Result, error) {
	dispatcher := &collectResponseDispatcher{
		server:      s,
		eventWriter: writer,
	}
	started := time.Now()
	if err := anthropicbackend.CollectResponse(
		dispatcher,
		nil,
		ctx,
		prepared.Request,
		prepared.Model,
		prepared.RequestID,
		started,
		prepared.JSONCoercion,
		true,
		prepared.TrackerKey,
	); err != nil {
		return adapterprovider.Result{}, err
	}
	if dispatcher.finalResponse == nil {
		return adapterprovider.Result{}, &anthropic.ExecuteError{
			Status:  http.StatusBadGateway,
			Code:    "upstream_error",
			Message: "anthropic provider collect path produced no final response",
		}
	}
	return anthropicProviderResultFromResponse(dispatcher.finalResponse), nil
}

func (s *Server) executeAnthropicPreparedStream(
	ctx context.Context,
	prepared anthropic.PreparedRequest,
	writer adapterprovider.EventWriter,
) (adapterprovider.Result, error) {
	streamWriter, ok := writer.(*providerStreamWriter)
	if !ok || streamWriter == nil {
		return adapterprovider.Result{}, &anthropic.ExecuteError{
			Status:  http.StatusInternalServerError,
			Code:    "internal_error",
			Message: "anthropic stream provider requires a streaming event writer",
		}
	}

	dispatcher := &streamResponseDispatcher{
		server:      s,
		sse:         &providerAnthropicSSEWriter{writer: streamWriter},
		eventWriter: writer,
	}
	started := time.Now()
	request := (&http.Request{}).WithContext(ctx)
	if err := anthropicbackend.StreamResponse(
		dispatcher,
		nil,
		request,
		prepared.Request,
		prepared.Model,
		prepared.RequestID,
		started,
		true,
		prepared.IncludeUsage,
		prepared.TrackerKey,
	); err != nil {
		return adapterprovider.Result{}, err
	}
	return adapterprovider.Result{SystemFingerprint: systemFingerprint}, nil
}

func writeAnthropicProviderError(w http.ResponseWriter, err error) {
	var execErr *anthropic.ExecuteError
	if errors.As(err, &execErr) {
		writeError(w, execErr.Status, execErr.Code, execErr.Message)
		return
	}
	writeError(w, http.StatusBadGateway, "upstream_error", err.Error())
}

func anthropicResolvedModelFromRequest(req adapterresolver.ResolvedRequest) adaptermodel.ResolvedModel {
	alias := req.Cursor.NormalizedModel
	if alias == "" {
		alias = req.OpenAI.Model
	}
	return adaptermodel.ResolvedModel{
		Alias:           alias,
		Backend:         adaptermodel.BackendAnthropic,
		ClaudeModel:     req.Model,
		Context:         req.ContextBudget.InputTokens,
		Effort:          req.Effort.String(),
		MaxOutputTokens: req.ContextBudget.OutputTokens,
		FamilySlug:      req.Family,
	}
}

func anthropicProviderResultFromResponse(resp *adapteropenai.ChatResponse) adapterprovider.Result {
	if resp == nil {
		return adapterprovider.Result{}
	}
	result := adapterprovider.Result{
		FinalResponse:     resp,
		SystemFingerprint: resp.SystemFingerprint,
	}
	if resp.Usage != nil {
		result.Usage = *resp.Usage
	}
	if len(resp.Choices) > 0 {
		result.FinishReason = resp.Choices[0].FinishReason
		if reasoning := resp.Choices[0].Message.Reasoning; reasoning != "" {
			result.ReasoningSignaled = true
			result.ReasoningVisible = true
			result.ReasoningSummary = reasoning
		}
	}
	return result
}

type collectResponseDispatcher struct {
	server        *Server
	finalResponse *adapteropenai.ChatResponse
	eventWriter   adapterprovider.EventWriter
}

func (d *collectResponseDispatcher) Log() *slog.Logger {
	return d.server.log
}

func (d *collectResponseDispatcher) EmitRequestStarted(ctx context.Context, model adaptermodel.ResolvedModel, _ string, reqID, modelID string, stream bool) {
	d.server.emitRequestStarted(ctx, model, "oauth", reqID, modelID, stream)
}

func (d *collectResponseDispatcher) EmitRequestStreamOpened(context.Context, adaptermodel.ResolvedModel, string, string, string, bool) {
}

func (d *collectResponseDispatcher) NewAnthropicSSEWriter(http.ResponseWriter) (anthropicbackend.ResponseSSEWriter, error) {
	return nil, fmt.Errorf("anthropic collect dispatcher does not support SSE writers")
}

func (d *collectResponseDispatcher) AnthropicStreamClient() anthropicbackend.StreamClient {
	return d.server.anthr
}

func (d *collectResponseDispatcher) SystemFingerprint() string {
	return systemFingerprint
}

func (d *collectResponseDispatcher) StreamChunkHasVisibleContent(chunk adapteropenai.StreamChunk) bool {
	return streamChunkHasVisibleContent(chunk)
}

func (d *collectResponseDispatcher) WriteEvent(ev adapterrender.Event) error {
	if d == nil || d.eventWriter == nil {
		return nil
	}
	return d.eventWriter.WriteEvent(ev)
}

func (d *collectResponseDispatcher) FlushEventWriter() error {
	if d == nil || d.eventWriter == nil {
		return nil
	}
	return d.eventWriter.Flush()
}

func (d *collectResponseDispatcher) CollectedEvents() []adapterrender.Event {
	collector, ok := d.eventWriter.(*providerCollectorWriter)
	if !ok || collector == nil {
		return nil
	}
	return collector.events
}

func (d *collectResponseDispatcher) TrackAnthropicContextUsage(key string, raw adapteropenai.Usage) anthropicbackend.TrackedUsage {
	tracked := d.server.ctxUsage.Track(key, raw)
	return anthropicbackend.TrackedUsage{
		Usage:      tracked.usage,
		RawPrompt:  tracked.rawPrompt,
		RawTotal:   tracked.rawTotal,
		RolledFrom: tracked.rolledFrom,
	}
}

func (d *collectResponseDispatcher) WriteJSON(_ http.ResponseWriter, _ int, resp adapteropenai.ChatResponse) {
	captured := resp
	d.finalResponse = &captured
}

func (d *collectResponseDispatcher) WriteErrorJSON(http.ResponseWriter, int, adapteropenai.ErrorResponse) {
}

func (d *collectResponseDispatcher) LogTerminal(ctx context.Context, ev adapterruntime.RequestEvent) {
	adapterruntime.LogTerminal(d.server.log, ctx, d.server.deps.RequestEvents, ev)
}

func (d *collectResponseDispatcher) LogCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, usage anthropic.Usage) {
	d.server.logCacheUsageAnthropic(ctx, backend, reqID, alias, usage)
}

func (d *collectResponseDispatcher) CacheTTL() string {
	return d.server.cfg.ClientIdentity.PromptCacheTTL
}

func (d *collectResponseDispatcher) NoticesEnabled() bool {
	return d.server.cfg.Notices.EnabledOrDefault()
}

func (d *collectResponseDispatcher) ClaimNotice(kind string, resetsAt time.Time) bool {
	return Claim(kind, resetsAt)
}

func (d *collectResponseDispatcher) UnclaimNotice(kind string, resetsAt time.Time) {
	Unclaim(kind, resetsAt)
}

type streamResponseDispatcher struct {
	server      *Server
	sse         anthropicbackend.ResponseSSEWriter
	eventWriter adapterprovider.EventWriter
}

func (d *streamResponseDispatcher) Log() *slog.Logger {
	return d.server.log
}

func (d *streamResponseDispatcher) EmitRequestStarted(ctx context.Context, model adaptermodel.ResolvedModel, _ string, reqID, modelID string, stream bool) {
	d.server.emitRequestStarted(ctx, model, "oauth", reqID, modelID, stream)
}

func (d *streamResponseDispatcher) EmitRequestStreamOpened(ctx context.Context, model adaptermodel.ResolvedModel, _ string, reqID, modelID string, stream bool) {
	d.server.emitRequestStreamOpened(ctx, model, "oauth", reqID, modelID, stream)
}

func (d *streamResponseDispatcher) NewAnthropicSSEWriter(http.ResponseWriter) (anthropicbackend.ResponseSSEWriter, error) {
	return d.sse, nil
}

func (d *streamResponseDispatcher) AnthropicStreamClient() anthropicbackend.StreamClient {
	return d.server.anthr
}

func (d *streamResponseDispatcher) SystemFingerprint() string {
	return systemFingerprint
}

func (d *streamResponseDispatcher) StreamChunkHasVisibleContent(chunk adapteropenai.StreamChunk) bool {
	return streamChunkHasVisibleContent(chunk)
}

func (d *streamResponseDispatcher) WriteEvent(ev adapterrender.Event) error {
	if d == nil || d.eventWriter == nil {
		return nil
	}
	return d.eventWriter.WriteEvent(ev)
}

func (d *streamResponseDispatcher) FlushEventWriter() error {
	if d == nil || d.eventWriter == nil {
		return nil
	}
	return d.eventWriter.Flush()
}

func (d *streamResponseDispatcher) CollectedEvents() []adapterrender.Event {
	return nil
}

func (d *streamResponseDispatcher) TrackAnthropicContextUsage(key string, raw adapteropenai.Usage) anthropicbackend.TrackedUsage {
	tracked := d.server.ctxUsage.Track(key, raw)
	return anthropicbackend.TrackedUsage{
		Usage:      tracked.usage,
		RawPrompt:  tracked.rawPrompt,
		RawTotal:   tracked.rawTotal,
		RolledFrom: tracked.rolledFrom,
	}
}

func (d *streamResponseDispatcher) WriteJSON(_ http.ResponseWriter, _ int, _ adapteropenai.ChatResponse) {
}

func (d *streamResponseDispatcher) WriteErrorJSON(_ http.ResponseWriter, _ int, _ adapteropenai.ErrorResponse) {
}

func (d *streamResponseDispatcher) LogTerminal(ctx context.Context, ev adapterruntime.RequestEvent) {
	adapterruntime.LogTerminal(d.server.log, ctx, d.server.deps.RequestEvents, ev)
}

func (d *streamResponseDispatcher) LogCacheUsageAnthropic(ctx context.Context, backend, reqID, alias string, usage anthropic.Usage) {
	d.server.logCacheUsageAnthropic(ctx, backend, reqID, alias, usage)
}

func (d *streamResponseDispatcher) CacheTTL() string {
	return d.server.cfg.ClientIdentity.PromptCacheTTL
}

func (d *streamResponseDispatcher) NoticesEnabled() bool {
	return d.server.cfg.Notices.EnabledOrDefault()
}

func (d *streamResponseDispatcher) ClaimNotice(kind string, resetsAt time.Time) bool {
	return Claim(kind, resetsAt)
}

func (d *streamResponseDispatcher) UnclaimNotice(kind string, resetsAt time.Time) {
	Unclaim(kind, resetsAt)
}

type providerAnthropicSSEWriter struct {
	writer *providerStreamWriter
}

func (w *providerAnthropicSSEWriter) WriteSSEHeaders() {
	if w == nil || w.writer == nil || w.writer.headersWritten {
		return
	}
	w.writer.sse.WriteSSEHeaders()
	w.writer.headersWritten = true
}

func (w *providerAnthropicSSEWriter) EmitStreamChunk(systemFingerprint string, chunk adapteropenai.StreamChunk) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("provider stream writer missing")
	}
	if !w.writer.headersWritten {
		w.WriteSSEHeaders()
	}
	return w.writer.sse.EmitStreamChunk(systemFingerprint, chunk)
}

func (w *providerAnthropicSSEWriter) EmitStreamError(body adapteropenai.ErrorBody) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("provider stream writer missing")
	}
	if !w.writer.headersWritten {
		w.WriteSSEHeaders()
	}
	return w.writer.sse.EmitStreamError(body)
}

func (w *providerAnthropicSSEWriter) WriteStreamDone() error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("provider stream writer missing")
	}
	return w.writer.sse.WriteStreamDone()
}

func (w *providerAnthropicSSEWriter) HasCommittedHeaders() bool {
	return w != nil && w.writer != nil && w.writer.headersWritten
}
