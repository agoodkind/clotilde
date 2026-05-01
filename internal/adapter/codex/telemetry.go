package codex

import (
	"context"
	"log/slog"

	"goodkind.io/clyde/internal/correlation"
)

type TransportTelemetry struct {
	RequestID                string
	CursorRequestID          string
	Correlation              correlation.Context
	Alias                    string
	UpstreamModel            string
	Transport                string
	ServiceTier              string
	MaxCompletion            *int
	PromptCacheKey           string
	ClientMetadata           map[string]string
	InputCount               int
	ToolCount                int
	NativeShellCount         int
	NativeCustomCount        int
	FunctionToolCount        int
	WebsocketWarmup          bool
	WebsocketPrewarmUsed     bool
	WebsocketPrewarmFailed   bool
	WebsocketConnectionReuse bool
	PreviousResponseID       string
	TurnStatePresent         bool
	FallbackToHTTP           bool
	ContextWindowError       bool
}

func LogTransportPrepared(ctx context.Context, log *slog.Logger, telemetry TransportTelemetry) {
	if log == nil {
		log = slog.Default()
	}
	attrs := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", "codex"),
		slog.String("request_id", telemetry.RequestID),
		slog.String("cursor_request_id", telemetry.CursorRequestID),
		slog.String("alias", telemetry.Alias),
		slog.String("model", telemetry.UpstreamModel),
		slog.String("transport", telemetry.Transport),
		slog.String("service_tier", telemetry.ServiceTier),
		slog.Any("max_completion_tokens", telemetry.MaxCompletion),
		slog.Bool("has_prompt_cache_key", telemetry.PromptCacheKey != ""),
		slog.Bool("has_client_metadata", len(telemetry.ClientMetadata) > 0),
		slog.Int("input_count", telemetry.InputCount),
		slog.Int("tool_count", telemetry.ToolCount),
		slog.Int("native_local_shell_count", telemetry.NativeShellCount),
		slog.Int("native_custom_count", telemetry.NativeCustomCount),
		slog.Int("function_tool_count", telemetry.FunctionToolCount),
		slog.Bool("websocket_warmup", telemetry.WebsocketWarmup),
		slog.Bool("websocket_prewarm_used", telemetry.WebsocketPrewarmUsed),
		slog.Bool("websocket_prewarm_failed", telemetry.WebsocketPrewarmFailed),
		slog.Bool("websocket_connection_reused", telemetry.WebsocketConnectionReuse),
		slog.Bool("has_previous_response_id", telemetry.PreviousResponseID != ""),
		slog.Bool("has_turn_state", telemetry.TurnStatePresent),
		slog.Bool("fallback_to_http", telemetry.FallbackToHTTP),
		slog.Bool("context_window_error", telemetry.ContextWindowError),
	}
	attrs = append(attrs, telemetry.Correlation.Attrs()...)
	log.LogAttrs(ctx, slog.LevelInfo, "adapter.codex.transport.prepared", attrs...)
}
