package codex

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestLogTransportPreparedIncludesParityFields(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))
	maxCompletion := 2048

	LogTransportPrepared(context.Background(), log, TransportTelemetry{
		RequestID:                "req-1",
		Alias:                    "clyde-gpt-5.4",
		UpstreamModel:            "gpt-5.4",
		Transport:                "responses_websocket",
		ServiceTier:              "priority",
		MaxCompletion:            &maxCompletion,
		PromptCacheKey:           "cursor:conv-123",
		ClientMetadata:           map[string]string{"x-codex-window-id": "cursor:conv-123:0"},
		InputCount:               3,
		ToolCount:                2,
		NativeShellCount:         1,
		NativeCustomCount:        0,
		FunctionToolCount:        1,
		WebsocketWarmup:          true,
		WebsocketPrewarmUsed:     true,
		WebsocketConnectionReuse: true,
		PreviousResponseID:       "resp-123",
		TurnStatePresent:         true,
		FallbackToHTTP:           false,
		ContextWindowError:       true,
	})

	out := buf.String()
	for _, want := range []string{
		`"transport":"responses_websocket"`,
		`"service_tier":"priority"`,
		`"has_previous_response_id":true`,
		`"has_turn_state":true`,
		`"websocket_warmup":true`,
		`"websocket_prewarm_used":true`,
		`"websocket_connection_reused":true`,
		`"context_window_error":true`,
		`"input_count":3`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q in %s", want, out)
		}
	}
}

