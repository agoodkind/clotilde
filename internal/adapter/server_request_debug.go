package adapter

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"

	"goodkind.io/clyde/internal/correlation"
	"goodkind.io/clyde/internal/slogger"
)

func (s *Server) withRequestDebug(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		corr := correlation.FromContext(r.Context())
		if corr.RequestID == "" {
			reqID := r.Header.Get(correlation.HeaderRequestID)
			if reqID == "" {
				reqID = newRequestID()
			}
			corr = correlation.FromHTTPHeader(r.Header, reqID)
		}
		ctx := correlation.WithContext(r.Context(), corr)
		r = r.WithContext(ctx)

		if s.logging.Body.Mode != "off" {
			s.logHTTPRequestDebug(ctx, r)
		}
		next(w, r)
	}
}

func (s *Server) logHTTPRequestDebug(ctx context.Context, r *http.Request) {
	body, readErr := readAndRestoreBody(r)
	bodyLimit := s.logging.Body.MaxKB * 1024
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("remote_addr", r.RemoteAddr),
		slog.String("user_agent", r.UserAgent()),
		slog.Any("headers", redactedHeaders(r.Header)),
		slog.Int("body_bytes", len(body)),
	}
	switch s.logging.Body.Mode {
	case "raw":
		raw, truncated := truncateBody(body, bodyLimit)
		if raw != "" {
			attrs = append(attrs, slog.String("body", raw))
		}
		if b64 := encodeBodyB64(body, bodyLimit); b64 != "" {
			attrs = append(attrs, slog.String("body_b64", b64))
		}
		if truncated {
			attrs = append(attrs, slog.Bool("body_truncated", true))
		}
	case "whitelist":
		raw, truncated := truncateBody(body, bodyLimit)
		if raw != "" {
			attrs = append(attrs, slog.String("body", raw))
		}
		if truncated {
			attrs = append(attrs, slog.Bool("body_truncated", true))
		}
	}
	if readErr != nil {
		attrs = append(attrs, slog.String("body_read_error", readErr.Error()))
	}
	attrs = append(attrs, correlation.AttrsFromContext(ctx)...)
	slogger.WithConcern(s.log, slogger.ConcernAdapterHTTPRaw).LogAttrs(ctx, slog.LevelDebug, "adapter.request.raw", attrs...)
}

func readAndRestoreBody(r *http.Request) ([]byte, error) {
	if r.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(r.Body)
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, err
}
