package adapter

import (
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"

	"goodkind.io/clyde/internal/correlation"
	"goodkind.io/clyde/internal/slogger"
)

func (s *Server) withAdapterErrorBoundary(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		reqID := strings.TrimSpace(r.Header.Get(correlation.HeaderRequestID))
		if reqID == "" {
			reqID = newRequestID()
		}
		corr := correlation.FromHTTPHeader(r.Header, reqID)
		corr.SetHTTPHeaders(r.Header)
		corr.SetHTTPHeaders(w.Header())
		ctx := correlation.WithContext(r.Context(), corr)
		r = r.WithContext(ctx)

		rw := &adapterRecoveryWriter{ResponseWriter: w}
		defer func() {
			if recovered := recover(); recovered != nil {
				attrs := []slog.Attr{
					slog.Any("err", recovered),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("remote_addr", r.RemoteAddr),
					slog.String("user_agent", r.UserAgent()),
					slog.String("stack", string(debug.Stack())),
					slog.Bool("response_started", rw.wroteHeader),
				}
				attrs = append(attrs, corr.Attrs()...)
				slogger.WithConcern(s.log, slogger.ConcernAdapterHTTPErrors).LogAttrs(ctx, slog.LevelError, "adapter.request.panic", attrs...)
				if rw.wroteHeader {
					return
				}
				s.respondAdapterError(w, r, adapterErrInternal("adapter panic while handling request", fmt.Errorf("panic: %v", recovered)))
			}
		}()
		next(rw, r)
	}
}

type adapterRecoveryWriter struct {
	http.ResponseWriter
	wroteHeader bool
}

func (w *adapterRecoveryWriter) WriteHeader(statusCode int) {
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *adapterRecoveryWriter) Write(body []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func (w *adapterRecoveryWriter) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *adapterRecoveryWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func correlationForRequest(r *http.Request) correlation.Context {
	corr := correlation.FromContext(r.Context())
	if corr.RequestID != "" {
		return corr
	}
	reqID := strings.TrimSpace(r.Header.Get(correlation.HeaderRequestID))
	if reqID == "" {
		reqID = newRequestID()
	}
	return correlation.FromHTTPHeader(r.Header, reqID)
}
