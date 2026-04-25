package tooltrans

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type EventKind string

const (
	EventAssistantTextDelta  EventKind = "assistant_text_delta"
	EventReasoningSignaled   EventKind = "reasoning_signaled"
	EventReasoningDelta      EventKind = "reasoning_delta"
	EventReasoningFinished   EventKind = "reasoning_finished"
	EventPlanUpdated         EventKind = "plan_updated"
	EventToolStarted         EventKind = "tool_started"
	EventToolProgress        EventKind = "tool_progress"
	EventToolCompleted       EventKind = "tool_completed"
	EventFileChangeStarted   EventKind = "file_change_started"
	EventFileChangeProgress  EventKind = "file_change_progress"
	EventFileChangeCompleted EventKind = "file_change_completed"
	EventNotice              EventKind = "notice"
	EventToolCallDelta       EventKind = "tool_call_delta"
)

type EventPlanStep struct {
	Step   string
	Status string
}

type Event struct {
	Kind            EventKind
	Text            string
	ReasoningKind   string
	SummaryIndex    *int
	ToolCalls       []OpenAIToolCall
	ItemID          string
	ItemType        string
	ItemStatus      string
	Command         string
	ToolName        string
	ServerName      string
	ChangeCount     int
	Completed       bool
	PlanExplanation string
	Plan            []EventPlanStep
}

type RendererState struct {
	ReasoningSignaled bool
	ReasoningVisible  bool
}

type EventRenderer struct {
	createdUnix           int64
	modelAlias            string
	reqID                 string
	backend               string
	log                   *slog.Logger
	seenRole              bool
	reasoningOpen         bool
	lastReasoningKind     string
	lastSummaryIdx        int
	haveSummaryIdx        bool
	pendingReasoningBreak bool
	reasoningSignaled     bool
	reasoningVisible      bool
}

func NewEventRenderer(reqID, modelAlias, backend string, log *slog.Logger) *EventRenderer {
	if log == nil {
		log = slog.Default()
	}
	return &EventRenderer{
		createdUnix: time.Now().Unix(),
		modelAlias:  modelAlias,
		reqID:       reqID,
		backend:     backend,
		log:         log,
	}
}

func (r *EventRenderer) State() RendererState {
	return RendererState{ReasoningSignaled: r.reasoningSignaled, ReasoningVisible: r.reasoningVisible}
}

func (r *EventRenderer) HandleEvent(ev Event) []OpenAIStreamChunk {
	r.logNormalized(ev)
	var out []OpenAIStreamChunk
	switch ev.Kind {
	case EventReasoningSignaled:
		r.reasoningSignaled = true
	case EventReasoningDelta:
		r.reasoningSignaled = true
		r.reasoningVisible = true
		chunk := r.renderReasoning(ev)
		if chunk != nil {
			out = append(out, *chunk)
		}
	case EventReasoningFinished:
		if r.reasoningSignaled && !r.reasoningVisible {
			if chunk := r.renderSyntheticReasoningPlaceholder(); chunk != nil {
				out = append(out, *chunk)
			}
		}
		if chunk := r.renderReasoningClose(); chunk != nil {
			out = append(out, *chunk)
		}
	case EventAssistantTextDelta:
		if chunk := r.renderReasoningClose(); chunk != nil {
			out = append(out, *chunk)
		}
		if chunk := r.renderText(ev.Text); chunk != nil {
			out = append(out, *chunk)
		}
	case EventNotice:
		if chunk := r.renderActivity(ev.Text, ev.Kind, ev.ItemType, ev.ItemID); chunk != nil {
			out = append(out, *chunk)
		}
	case EventPlanUpdated:
		if chunk := r.renderActivity(formatPlanUpdate(ev.PlanExplanation, ev.Plan), ev.Kind, ev.ItemType, ev.ItemID); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolStarted, EventToolCompleted:
		if chunk := r.renderActivity(formatToolLifecycle(ev), ev.Kind, ev.ItemType, ev.ItemID); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolProgress, EventFileChangeProgress:
		if chunk := r.renderActivity(ev.Text, ev.Kind, ev.ItemType, ev.ItemID); chunk != nil {
			out = append(out, *chunk)
		}
	case EventFileChangeStarted, EventFileChangeCompleted:
		if chunk := r.renderActivity(formatFileChangeLifecycle(ev), ev.Kind, ev.ItemType, ev.ItemID); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolCallDelta:
		if chunk := r.renderToolCalls(ev.ToolCalls); chunk != nil {
			out = append(out, *chunk)
		}
	}
	for _, ch := range out {
		r.logRender(ev, ch)
	}
	return out
}

func (r *EventRenderer) renderText(text string) *OpenAIStreamChunk {
	if strings.TrimSpace(text) == "" && text == "" {
		return nil
	}
	delta := OpenAIStreamDelta{Content: text}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderToolCalls(toolCalls []OpenAIToolCall) *OpenAIStreamChunk {
	if len(toolCalls) == 0 {
		return nil
	}
	delta := OpenAIStreamDelta{ToolCalls: toolCalls}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderReasoning(ev Event) *OpenAIStreamChunk {
	text := strings.TrimSpace(ev.Text)
	if text == "" && ev.Text == "" {
		return nil
	}
	open := !r.reasoningOpen
	contentOut := FormatThinkingInlineDelta(open, r.decorateReasoningDelta(ev))
	r.reasoningOpen = true
	delta := OpenAIStreamDelta{Content: contentOut}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) decorateReasoningDelta(ev Event) string {
	prefix := ""
	kind := strings.TrimSpace(ev.ReasoningKind)
	if kind == "" {
		kind = "text"
	}
	if r.pendingReasoningBreak {
		prefix = "\n\n"
		r.pendingReasoningBreak = false
	} else if r.lastReasoningKind != "" && r.lastReasoningKind != kind {
		prefix = "\n\n"
	}
	if kind == "summary" && strings.HasPrefix(strings.TrimSpace(ev.Text), "**") {
		prefix = "\n\n"
	}
	if ev.SummaryIndex != nil {
		if r.haveSummaryIdx && r.lastSummaryIdx != *ev.SummaryIndex {
			prefix = "\n\n"
		}
		r.lastSummaryIdx = *ev.SummaryIndex
		r.haveSummaryIdx = true
	}
	r.lastReasoningKind = kind
	return prefix + ev.Text
}

func (r *EventRenderer) renderReasoningClose() *OpenAIStreamChunk {
	if !r.reasoningOpen {
		return nil
	}
	r.reasoningOpen = false
	ch := r.baseChunk(OpenAIStreamDelta{Content: ThinkingInlineClose()})
	return &ch
}

func (r *EventRenderer) renderSyntheticReasoningPlaceholder() *OpenAIStreamChunk {
	if r.reasoningOpen || r.reasoningVisible {
		return nil
	}
	delta := OpenAIStreamDelta{Content: ThinkingInlineOpen() + ThinkingInlineClose()}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderActivity(text string, kind EventKind, itemType, itemID string) *OpenAIStreamChunk {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if r.reasoningOpen {
		// do not interleave activity inside an open reasoning block
		return nil
	}
	delta := OpenAIStreamDelta{Content: FormatActivityDelta(text)}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) baseChunk(delta OpenAIStreamDelta) OpenAIStreamChunk {
	return OpenAIStreamChunk{
		ID:      r.reqID,
		Object:  "chat.completion.chunk",
		Created: r.createdUnix,
		Model:   r.modelAlias,
		Choices: []OpenAIStreamChoice{{Index: 0, Delta: delta}},
	}
}

func FormatActivityDelta(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return "<!--clyde-activity-->\n\n" + text + "\n\n<!--/clyde-activity-->\n\n"
}

func formatPlanUpdate(explanation string, plan []EventPlanStep) string {
	lines := make([]string, 0, len(plan)+1)
	explanation = strings.TrimSpace(explanation)
	if explanation != "" {
		lines = append(lines, "Progress update: "+explanation)
	} else {
		lines = append(lines, "Progress update:")
	}
	for _, step := range plan {
		label := strings.TrimSpace(step.Step)
		if label == "" {
			continue
		}
		status := strings.TrimSpace(step.Status)
		prefix := "-"
		switch status {
		case "completed":
			prefix = "- [done]"
		case "inProgress", "in_progress":
			prefix = "- [doing]"
		case "pending":
			prefix = "- [todo]"
		}
		lines = append(lines, prefix+" "+label)
	}
	return strings.Join(lines, "\n")
}

func formatToolLifecycle(ev Event) string {
	name := strings.TrimSpace(ev.ToolName)
	if name == "" {
		name = strings.Trim(strings.Join([]string{ev.ServerName, ev.ItemType}, "/"), "/")
	}
	if name == "" {
		name = "tool"
	}
	action := "Tool"
	switch ev.Kind {
	case EventToolStarted:
		action = "Tool started"
	case EventToolCompleted:
		if ev.ItemStatus != "" && ev.ItemStatus != "completed" {
			action = "Tool " + ev.ItemStatus
		} else {
			action = "Tool completed"
		}
	}
	return fmt.Sprintf("%s: `%s`", action, name)
}

func formatFileChangeLifecycle(ev Event) string {
	count := ev.ChangeCount
	if count < 1 {
		count = 1
	}
	switch ev.Kind {
	case EventFileChangeStarted:
		return fmt.Sprintf("File change started: %d file(s)", count)
	case EventFileChangeCompleted:
		if ev.ItemStatus != "" && ev.ItemStatus != "completed" {
			return fmt.Sprintf("File change %s: %d file(s)", ev.ItemStatus, count)
		}
		return fmt.Sprintf("File change completed: %d file(s)", count)
	default:
		return ""
	}
}

func (r *EventRenderer) logNormalized(ev Event) {
	r.log.LogAttrs(context.Background(), slog.LevelDebug, "adapter.event.normalized",
		slog.String("component", "adapter"),
		slog.String("subcomponent", "renderer"),
		slog.String("request_id", r.reqID),
		slog.String("backend", r.backend),
		slog.String("model", r.modelAlias),
		slog.String("alias", r.modelAlias),
		slog.String("event_kind", string(ev.Kind)),
		slog.String("item_type", ev.ItemType),
		slog.String("item_id", ev.ItemID),
		slog.Bool("reasoning_signaled", r.reasoningSignaled || ev.Kind == EventReasoningSignaled || ev.Kind == EventReasoningDelta),
		slog.Bool("reasoning_visible", r.reasoningVisible || ev.Kind == EventReasoningDelta),
		slog.Int("delta_len", len(ev.Text)),
	)
}

func (r *EventRenderer) logRender(ev Event, ch OpenAIStreamChunk) {
	delta := OpenAIStreamDelta{}
	if len(ch.Choices) > 0 {
		delta = ch.Choices[0].Delta
	}
	r.log.LogAttrs(context.Background(), slog.LevelDebug, "adapter.render.event",
		slog.String("component", "adapter"),
		slog.String("subcomponent", "renderer"),
		slog.String("request_id", r.reqID),
		slog.String("backend", r.backend),
		slog.String("model", r.modelAlias),
		slog.String("alias", r.modelAlias),
		slog.String("event_kind", string(ev.Kind)),
		slog.String("render_policy", renderPolicyForEvent(ev.Kind)),
		slog.Int("delta_len", len(delta.Content)+len(delta.Reasoning)+len(delta.ReasoningContent)),
	)
}

func renderPolicyForEvent(kind EventKind) string {
	switch kind {
	case EventReasoningDelta, EventReasoningFinished, EventReasoningSignaled:
		return "thinking_inline"
	case EventPlanUpdated, EventToolStarted, EventToolProgress, EventToolCompleted, EventFileChangeStarted, EventFileChangeProgress, EventFileChangeCompleted, EventNotice:
		return "activity_sentinel"
	case EventToolCallDelta:
		return "tool_call_delta"
	default:
		return "content_delta"
	}
}
