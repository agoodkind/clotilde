package codex

import (
	"context"
	"log/slog"
	"strings"
)

func LogToolingEvent(log *slog.Logger, ctx context.Context, requestID, event string, attrs ...slog.Attr) {
	attrs = append([]slog.Attr{slog.String("event", event)}, attrs...)
	logTransportEvent(log, ctx, requestID, "adapter.codex.tooling.event", attrs...)
}

func logTransportEvent(log *slog.Logger, ctx context.Context, requestID, msg string, attrs ...slog.Attr) {
	if log == nil {
		log = slog.Default()
	}
	base := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", "codex"),
		slog.String("request_id", requestID),
	}
	base = append(base, attrs...)
	log.LogAttrs(ctx, slog.LevelDebug, msg, base...)
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, _ := m[key].(string)
	return strings.TrimSpace(v)
}
