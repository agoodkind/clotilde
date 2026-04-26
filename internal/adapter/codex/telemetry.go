package codex

import (
	"context"
	"log/slog"
)

type TransportTelemetry struct {
	RequestID          string
	Alias              string
	UpstreamModel      string
	Transport          string
	ServiceTier        string
	MaxCompletion      *int
	PromptCacheKey     string
	ClientMetadata     map[string]string
	InputCount         int
	ToolCount          int
	NativeShellCount   int
	NativeCustomCount  int
	FunctionToolCount  int
	WebsocketWarmup    bool
	PreviousResponseID string
	FallbackToHTTP     bool
	ContextWindowError bool
}

type ContinuationTelemetry struct {
	RequestID          string
	Alias              string
	Transport          string
	Key                string
	Hit                bool
	MissReason         string
	FingerprintMatch   bool
	PreviousResponseID string
	IncrementalCount   int
}

func LogTransportPrepared(ctx context.Context, log *slog.Logger, telemetry TransportTelemetry) {
	if log == nil {
		log = slog.Default()
	}
	log.InfoContext(ctx, "adapter.codex.transport.prepared",
		"component", "adapter",
		"subcomponent", "codex",
		"request_id", telemetry.RequestID,
		"alias", telemetry.Alias,
		"model", telemetry.UpstreamModel,
		"transport", telemetry.Transport,
		"service_tier", telemetry.ServiceTier,
		"max_completion_tokens", telemetry.MaxCompletion,
		"has_prompt_cache_key", telemetry.PromptCacheKey != "",
		"has_client_metadata", len(telemetry.ClientMetadata) > 0,
		"input_count", telemetry.InputCount,
		"tool_count", telemetry.ToolCount,
		"native_local_shell_count", telemetry.NativeShellCount,
		"native_custom_count", telemetry.NativeCustomCount,
		"function_tool_count", telemetry.FunctionToolCount,
		"websocket_warmup", telemetry.WebsocketWarmup,
		"has_previous_response_id", telemetry.PreviousResponseID != "",
		"fallback_to_http", telemetry.FallbackToHTTP,
		"context_window_error", telemetry.ContextWindowError,
	)
}

func LogContinuationDecision(ctx context.Context, log *slog.Logger, telemetry ContinuationTelemetry) {
	if log == nil {
		log = slog.Default()
	}
	log.InfoContext(ctx, "adapter.codex.continuation.decided",
		"component", "adapter",
		"subcomponent", "codex",
		"request_id", telemetry.RequestID,
		"alias", telemetry.Alias,
		"transport", telemetry.Transport,
		"has_key", telemetry.Key != "",
		"hit", telemetry.Hit,
		"miss_reason", telemetry.MissReason,
		"fingerprint_match", telemetry.FingerprintMatch,
		"has_previous_response_id", telemetry.PreviousResponseID != "",
		"incremental_input_count", telemetry.IncrementalCount,
	)
}
