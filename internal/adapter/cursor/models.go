package cursor

import (
	"log/slog"
	"strings"
)

type RequestPathKind string

const (
	RequestPathForeground RequestPathKind = "foreground"
	RequestPathBackground RequestPathKind = "background"
	RequestPathResume     RequestPathKind = "resume"
	RequestPathSubagent   RequestPathKind = "subagent"
)

func NormalizeModelAlias(rawModel string) string {
	model := strings.TrimSpace(rawModel)
	lower := strings.ToLower(model)

	if strings.HasPrefix(lower, "clyde-gpt-") || strings.HasPrefix(lower, "clyde-codex-") {
		for _, suffix := range []string{"-low", "-medium", "-high", "-xhigh"} {
			if strings.HasSuffix(lower, suffix) {
				return model[:len(model)-len(suffix)]
			}
		}
	}

	return model
}

func RequestPath(req Request) RequestPathKind {
	return RequestPathForeground
}

func BoundaryLogAttrs(req Request, rawModel string, toolNames []string) []slog.Attr {
	normalizedModel := req.NormalizedModel
	if normalizedModel == "" {
		normalizedModel = NormalizeModelAlias(rawModel)
	}
	mode := DetectMode(req)
	if len(toolNames) == 0 {
		toolNames = req.RawToolNames
	}

	attrs := []slog.Attr{
		slog.String("cursor_request_path", string(req.PathKind)),
		slog.String("cursor_raw_model", strings.TrimSpace(rawModel)),
		slog.String("cursor_normalized_model", normalizedModel),
		slog.Any("cursor_raw_tool_names", toolNames),
		slog.String("cursor_mode", string(mode)),
		slog.Bool("cursor_can_spawn_agent", req.CanSpawnAgent),
		slog.Bool("cursor_can_switch_mode", req.CanSwitchMode),
	}

	if req.ConversationID != "" {
		attrs = append(attrs, slog.String("cursor_conversation_id", req.ConversationID))
	}
	if req.RequestID != "" {
		attrs = append(attrs, slog.String("cursor_request_id", req.RequestID))
	}

	return attrs
}

func hasRawToolName(toolNames []string, want string) bool {
	want = strings.TrimSpace(want)
	for _, name := range toolNames {
		if strings.TrimSpace(name) == want {
			return true
		}
	}
	return false
}
