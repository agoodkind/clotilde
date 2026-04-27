package codex

import (
	"context"
	"log/slog"
)

type TransportTelemetry struct {
	RequestID                string
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

type ContinuationTelemetry struct {
	RequestID             string
	Alias                 string
	Transport             string
	Key                   string
	Hit                   bool
	MissReason            string
	FingerprintMatch      bool
	PreviousResponseID    string
	IncrementalCount      int
	ExpectedEventCount    int
	CurrentEventCount     int
	BaselineMatchStart    int
	BaselineMatchEnd      int
	MismatchExpectedIndex int
	MismatchCurrentIndex  int
	MismatchExpectedItem  int
	MismatchCurrentItem   int
	MismatchExpected      string
	MismatchCurrent       string
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
		"websocket_prewarm_used", telemetry.WebsocketPrewarmUsed,
		"websocket_prewarm_failed", telemetry.WebsocketPrewarmFailed,
		"websocket_connection_reused", telemetry.WebsocketConnectionReuse,
		"has_previous_response_id", telemetry.PreviousResponseID != "",
		"has_turn_state", telemetry.TurnStatePresent,
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
		"expected_event_count", telemetry.ExpectedEventCount,
		"current_event_count", telemetry.CurrentEventCount,
		"baseline_match_start", telemetry.BaselineMatchStart,
		"baseline_match_end", telemetry.BaselineMatchEnd,
		"mismatch_expected_event_index", telemetry.MismatchExpectedIndex,
		"mismatch_current_event_index", telemetry.MismatchCurrentIndex,
		"mismatch_expected_item_index", telemetry.MismatchExpectedItem,
		"mismatch_current_item_index", telemetry.MismatchCurrentItem,
		"mismatch_expected", telemetry.MismatchExpected,
		"mismatch_current", telemetry.MismatchCurrent,
	)
}
