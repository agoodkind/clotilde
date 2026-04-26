package codex

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

func PlanEvent(explanation string, plan []map[string]string) (adapterrender.Event, bool) {
	ev := adapterrender.Event{
		Kind:            adapterrender.EventPlanUpdated,
		PlanExplanation: strings.TrimSpace(explanation),
		Plan:            make([]adapterrender.EventPlanStep, 0, len(plan)),
	}
	for _, step := range plan {
		label := strings.TrimSpace(step["step"])
		if label == "" {
			continue
		}
		ev.Plan = append(ev.Plan, adapterrender.EventPlanStep{
			Step:   label,
			Status: strings.TrimSpace(step["status"]),
		})
	}
	if ev.PlanExplanation == "" && len(ev.Plan) == 0 {
		return adapterrender.Event{}, false
	}
	return ev, true
}

func LifecycleEvent(item map[string]any, completed bool) (adapterrender.Event, bool) {
	itemType := itemType(item)
	status := itemStatus(item)
	switch itemType {
	case "commandExecution", "mcpToolCall", "dynamicToolCall", "collabAgentToolCall", "contextCompaction":
		kind := adapterrender.EventToolStarted
		if completed {
			kind = adapterrender.EventToolCompleted
		}
		return adapterrender.Event{
			Kind:       kind,
			ItemType:   itemType,
			ItemStatus: status,
			ItemID:     mapString(item, "id"),
			ToolName:   toolName(item),
			ServerName: mapString(item, "server"),
			Command:    mapString(item, "command"),
			Completed:  completed,
		}, true
	case "fileChange":
		kind := adapterrender.EventFileChangeStarted
		if completed {
			kind = adapterrender.EventFileChangeCompleted
		}
		return adapterrender.Event{
			Kind:        kind,
			ItemType:    itemType,
			ItemStatus:  status,
			ItemID:      mapString(item, "id"),
			ChangeCount: fileChangeCount(item),
			Completed:   completed,
		}, true
	default:
		return adapterrender.Event{}, false
	}
}

func ProgressEvent(method, itemID, text string) (adapterrender.Event, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return adapterrender.Event{}, false
	}
	switch method {
	case "item/fileChange/outputDelta", "item/fileChange/patchUpdated":
		return adapterrender.Event{
			Kind:     adapterrender.EventFileChangeProgress,
			ItemID:   itemID,
			ItemType: "fileChange",
			Text:     text,
		}, true
	default:
		return adapterrender.Event{
			Kind:   adapterrender.EventToolProgress,
			ItemID: itemID,
			Text:   text,
		}, true
	}
}

func LogReasoningEvent(log *slog.Logger, ctx context.Context, requestID, event string, attrs ...slog.Attr) {
	attrs = append([]slog.Attr{slog.String("event", event)}, attrs...)
	logTransportEvent(log, ctx, requestID, "adapter.codex.reasoning.event", attrs...)
}

func LogToolingEvent(log *slog.Logger, ctx context.Context, requestID, event string, attrs ...slog.Attr) {
	attrs = append([]slog.Attr{slog.String("event", event)}, attrs...)
	logTransportEvent(log, ctx, requestID, "adapter.codex.tooling.event", attrs...)
}

func LogProtocolEvent(ctx context.Context, requestID, backend, event string, attrs ...slog.Attr) {
	base := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", backend),
		slog.String("request_id", requestID),
		slog.String("backend", backend),
		slog.String("event", event),
	}
	base = append(base, attrs...)
	slog.LogAttrs(ctx, slog.LevelDebug, "adapter.protocol.event", base...)
}

func EmitRendered(
	renderer *adapterrender.EventRenderer,
	ev adapterrender.Event,
	emit func(adapteropenai.StreamChunk) error,
	assistantText *strings.Builder,
) error {
	for _, ch := range renderer.HandleEvent(ev) {
		if assistantText != nil && len(ch.Choices) > 0 {
			assistantText.WriteString(ch.Choices[0].Delta.Content)
		}
		if err := emit(ch); err != nil {
			return err
		}
	}
	return nil
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

func mapSlice(m map[string]any, key string) []any {
	if m == nil {
		return nil
	}
	v, _ := m[key].([]any)
	return v
}

func itemType(item map[string]any) string {
	return mapString(item, "type")
}

func itemStatus(item map[string]any) string {
	return mapString(item, "status")
}

func fileChangeCount(item map[string]any) int {
	changes := mapSlice(item, "changes")
	count := len(changes)
	if count == 0 {
		count = 1
	}
	return count
}

func toolName(item map[string]any) string {
	if cmd := mapString(item, "command"); cmd != "" {
		return cmd
	}
	if tool := mapString(item, "tool"); tool != "" {
		return tool
	}
	server := mapString(item, "server")
	tool := mapString(item, "tool")
	name := strings.Trim(strings.Join([]string{server, tool}, "/"), "/")
	if name != "" {
		return name
	}
	if typ := itemType(item); typ != "" {
		return typ
	}
	return "tool"
}

func ParseTransportStream(body io.Reader, requestID, alias string, log *slog.Logger, emit func(adapteropenai.StreamChunk) error) (RunResult, error) {
	renderer := adapterrender.NewEventRenderer(requestID, alias, "codex", log)
	return ParseSSE(bufio.NewReader(body), renderer, emit)
}
