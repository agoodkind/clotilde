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

func TestLogContinuationDecisionIncludesMismatchDiagnostics(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	LogContinuationDecision(context.Background(), log, ContinuationTelemetry{
		RequestID:             "req-1",
		Alias:                 "gpt-5.4",
		Transport:             "responses_websocket",
		Key:                   "cursor:conv-123",
		Hit:                   false,
		MissReason:            "output_item_baseline_mismatch",
		FingerprintMatch:      true,
		PreviousResponseID:    "resp-123",
		ExpectedEventCount:    1,
		CurrentEventCount:     4,
		BaselineMatchStart:    -1,
		BaselineMatchEnd:      -1,
		MismatchExpectedIndex: 0,
		MismatchCurrentIndex:  2,
		MismatchExpectedItem:  0,
		MismatchCurrentItem:   2,
		MismatchExpected:      "kind=tool_call id=call_pwd name=Shell payload_len=35",
		MismatchCurrent:       "kind=tool_call id=call_ls name=Shell payload_len=52",
	})

	out := buf.String()
	for _, want := range []string{
		`"miss_reason":"output_item_baseline_mismatch"`,
		`"expected_event_count":1`,
		`"current_event_count":4`,
		`"baseline_match_start":-1`,
		`"mismatch_expected_event_index":0`,
		`"mismatch_current_event_index":2`,
		`"mismatch_expected":"kind=tool_call id=call_pwd name=Shell payload_len=35"`,
		`"mismatch_current":"kind=tool_call id=call_ls name=Shell payload_len=52"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log output missing %q in %s", want, out)
		}
	}
}
