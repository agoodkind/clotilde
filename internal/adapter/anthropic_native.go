package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"goodkind.io/clyde/internal/adapter/anthropic"
	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	"goodkind.io/clyde/internal/correlation"
)

const maxAnthropicMessagesBodyBytes = 8 << 20

func (s *Server) handleAnthropicMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "POST required")
		return
	}
	if s.anthropicProvider == nil {
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "anthropic backend is not enabled in [adapter]")
		return
	}

	reqID := newRequestID()
	corr := correlation.FromHTTPHeader(r.Header, reqID)
	corr.SetHTTPHeaders(w.Header())
	ctx := correlation.WithContext(r.Context(), corr)
	r = r.WithContext(ctx)
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAnthropicMessagesBodyBytes))
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "failed to read body")
		return
	}

	var req anthropic.Request
	if err := json.Unmarshal(body, &req); err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}

	model, _, err := s.registry.Resolve(req.Model, "")
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	nativeClaudeModel := isNativeClaudeModelID(req.Model)
	if !nativeClaudeModel && model.Backend != BackendAnthropic && model.Backend != BackendClaude {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "model does not resolve to the anthropic backend")
		return
	}
	if nativeClaudeModel && model.Backend != BackendAnthropic && model.Backend != BackendClaude {
		model = ResolvedModel{
			Alias:       strings.TrimSpace(req.Model),
			Backend:     BackendClaude,
			ClaudeModel: strings.TrimSpace(req.Model),
		}
	}
	req.Model = anthropicIngressWireModel(req.Model, model)

	attrs := []slog.Attr{
		slog.String("request_id", reqID),
		slog.String("path", r.URL.Path),
		slog.String("model", req.Model),
		slog.Bool("stream", req.Stream),
		slog.Int("body_bytes", len(body)),
	}
	attrs = append(attrs, corr.Attrs()...)
	s.log.LogAttrs(ctx, slog.LevelInfo, "adapter.anthropic.ingress", attrs...)

	prepared := anthropic.PreparedRequest{
		Request:       req,
		Model:         anthropicIngressResolvedModel(model),
		RequestID:     reqID,
		NativeIngress: true,
	}
	ctx = anthropic.WithRequestID(ctx, reqID)
	if req.Stream {
		streamWriter, streamErr := newNativeAnthropicStreamWriter(w)
		if streamErr != nil {
			writeAnthropicError(w, http.StatusInternalServerError, "api_error", streamErr.Error())
			return
		}
		if _, err := s.anthropicProvider.ExecutePrepared(ctx, prepared, streamWriter); err != nil {
			writeAnthropicIngressProviderError(w, err)
		}
		return
	}

	collector := newNativeAnthropicJSONWriter()
	if _, err := s.anthropicProvider.ExecutePrepared(ctx, prepared, collector); err != nil {
		writeAnthropicIngressProviderError(w, err)
		return
	}
	collector.writeTo(w)
}

func (s *Server) handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAnthropicError(w, http.StatusMethodNotAllowed, "invalid_request_error", "POST required")
		return
	}
	writeAnthropicError(w, http.StatusNotImplemented, "not_supported_error", "/v1/messages/count_tokens is not implemented yet on the adapter Anthropic ingress")
}

func isNativeClaudeModelID(model string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "claude-")
}

func anthropicIngressWireModel(requested string, model ResolvedModel) string {
	if isNativeClaudeModelID(requested) {
		return anthropicbackend.StripContextSuffix(requested)
	}
	return anthropicbackend.StripContextSuffix(model.ClaudeModel)
}

func anthropicIngressResolvedModel(model ResolvedModel) adaptermodel.ResolvedModel {
	return adaptermodel.ResolvedModel{
		Alias:           model.Alias,
		Backend:         adaptermodel.BackendAnthropic,
		ClaudeModel:     model.ClaudeModel,
		Context:         model.Context,
		Effort:          "",
		MaxOutputTokens: model.MaxOutputTokens,
	}
}

func writeAnthropicError(w http.ResponseWriter, code int, errType, message string) {
	payload, err := json.Marshal(anthropic.ErrorEnvelope{
		Type: "error",
		Error: anthropic.ErrorDetail{
			Type:    errType,
			Message: message,
		},
	})
	if err != nil {
		http.Error(w, `{"type":"error","error":{"type":"api_error","message":"failed to encode error"}}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(payload)
}

func writeAnthropicIngressProviderError(w http.ResponseWriter, err error) {
	var execErr *anthropic.ExecuteError
	if errors.As(err, &execErr) {
		writeAnthropicError(w, execErr.Status, anthropicErrorType(execErr.Status, execErr.Code), execErr.Message)
		return
	}
	if upstreamErr, ok := anthropic.AsUpstreamError(err); ok {
		status := upstreamErr.Status
		if status == 0 {
			status = http.StatusBadGateway
		}
		writeAnthropicError(w, status, anthropicErrorType(status, ""), upstreamErr.Message)
		return
	}
	writeAnthropicError(w, http.StatusBadGateway, "api_error", err.Error())
}

func anthropicErrorType(status int, code string) string {
	if strings.TrimSpace(code) == "not_supported_error" {
		return "not_supported_error"
	}
	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed:
		return "invalid_request_error"
	case http.StatusUnauthorized, http.StatusForbidden:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

type nativeAnthropicJSONWriter struct {
	status int
	header http.Header
	body   []byte
}

func newNativeAnthropicJSONWriter() *nativeAnthropicJSONWriter {
	return &nativeAnthropicJSONWriter{
		status: http.StatusOK,
		header: make(http.Header),
	}
}

func (w *nativeAnthropicJSONWriter) WriteEvent(adapterrender.Event) error {
	return nil
}

func (w *nativeAnthropicJSONWriter) Flush() error {
	return nil
}

func (w *nativeAnthropicJSONWriter) capture(status int, header http.Header, body []byte) error {
	if w == nil {
		return fmt.Errorf("native anthropic writer missing")
	}
	w.status = status
	w.body = append(w.body[:0], body...)
	w.header = make(http.Header, len(header))
	for key, values := range header {
		cloned := append([]string(nil), values...)
		w.header[key] = cloned
	}
	return nil
}

func (w *nativeAnthropicJSONWriter) writeTo(dst http.ResponseWriter) {
	if w == nil {
		writeAnthropicError(dst, http.StatusBadGateway, "api_error", "anthropic native collect path produced no response")
		return
	}
	for key, values := range w.header {
		for _, value := range values {
			dst.Header().Add(key, value)
		}
	}
	if dst.Header().Get("Content-Type") == "" {
		dst.Header().Set("Content-Type", "application/json")
	}
	dst.WriteHeader(w.status)
	_, _ = dst.Write(w.body)
}

type nativeAnthropicStreamWriter struct {
	w         http.ResponseWriter
	flusher   http.Flusher
	committed bool
}

func newNativeAnthropicStreamWriter(w http.ResponseWriter) (*nativeAnthropicStreamWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support streaming")
	}
	return &nativeAnthropicStreamWriter{
		w:       w,
		flusher: flusher,
	}, nil
}

func (w *nativeAnthropicStreamWriter) WriteEvent(adapterrender.Event) error {
	return nil
}

func (w *nativeAnthropicStreamWriter) Flush() error {
	if w != nil && w.flusher != nil {
		w.flusher.Flush()
	}
	return nil
}

func (w *nativeAnthropicStreamWriter) commit(header http.Header) {
	if w == nil || w.committed {
		return
	}
	for key, values := range header {
		for _, value := range values {
			w.w.Header().Add(key, value)
		}
	}
	if w.w.Header().Get("Content-Type") == "" {
		w.w.Header().Set("Content-Type", "text/event-stream")
	}
	w.w.WriteHeader(http.StatusOK)
	w.committed = true
	if w.flusher != nil {
		w.flusher.Flush()
	}
}

func (w *nativeAnthropicStreamWriter) write(chunk []byte) error {
	if w == nil {
		return fmt.Errorf("native anthropic stream writer missing")
	}
	if !w.committed {
		w.commit(http.Header{"Content-Type": {"text/event-stream"}})
	}
	if _, err := w.w.Write(chunk); err != nil {
		return err
	}
	return w.Flush()
}

func (w *nativeAnthropicStreamWriter) relay(resp *http.Response) error {
	if w == nil {
		return fmt.Errorf("native anthropic stream writer missing")
	}
	defer resp.Body.Close()
	w.commit(resp.Header.Clone())
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if writeErr := w.write(buf[:n]); writeErr != nil {
				return writeErr
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
}

var (
	_ adapterprovider.EventWriter = (*nativeAnthropicJSONWriter)(nil)
	_ adapterprovider.EventWriter = (*nativeAnthropicStreamWriter)(nil)
)
