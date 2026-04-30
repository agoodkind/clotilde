package render

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type EventKind string

const (
	EventAssistantTextDelta    EventKind = "assistant_text_delta"
	EventAssistantRefusalDelta EventKind = "assistant_refusal_delta"
	EventReasoningSignaled     EventKind = "reasoning_signaled"
	EventReasoningDelta        EventKind = "reasoning_delta"
	EventReasoningFinished     EventKind = "reasoning_finished"
	EventPlanUpdated           EventKind = "plan_updated"
	EventToolStarted           EventKind = "tool_started"
	EventToolProgress          EventKind = "tool_progress"
	EventToolCompleted         EventKind = "tool_completed"
	EventFileChangeStarted     EventKind = "file_change_started"
	EventFileChangeProgress    EventKind = "file_change_progress"
	EventFileChangeCompleted   EventKind = "file_change_completed"
	EventNotice                EventKind = "notice"
	EventToolCallDelta         EventKind = "tool_call_delta"
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
	ToolCalls       []adapteropenai.ToolCall
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
	suppressed            map[EventKind]*deltaSummary
	seenRole              bool
	reasoningOpen         bool
	lastReasoningKind     string
	lastSummaryIdx        int
	haveSummaryIdx        bool
	pendingReasoningBreak bool
	reasoningSignaled     bool
	reasoningVisible      bool
}

type deltaSummary struct {
	Count        int
	Chars        int
	MaxChars     int
	ToolCalls    int
	ToolArgChars int
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

func (r *EventRenderer) RequestID() string { return r.reqID }

func (r *EventRenderer) CreatedUnix() int64 { return r.createdUnix }

func (r *EventRenderer) ModelAlias() string { return r.modelAlias }

func (r *EventRenderer) Flush() {
	r.flushSuppressedEventSummaries()
}

func (r *EventRenderer) HandleEvent(ev Event) []adapteropenai.StreamChunk {
	logEvent := shouldLogEvent(ev)
	if logEvent {
		r.flushSuppressedEventSummaries()
		r.logNormalized(ev)
	} else {
		r.recordSuppressedEvent(ev)
	}
	var out []adapteropenai.StreamChunk
	switch ev.Kind {
	case EventReasoningSignaled:
		r.reasoningSignaled = true
	case EventReasoningDelta:
		r.reasoningSignaled = true
		r.reasoningVisible = true
		if chunk := r.renderReasoning(ev); chunk != nil {
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
	case EventAssistantRefusalDelta:
		if chunk := r.renderReasoningClose(); chunk != nil {
			out = append(out, *chunk)
		}
		if chunk := r.renderRefusal(ev.Text); chunk != nil {
			out = append(out, *chunk)
		}
	case EventNotice:
		if chunk := r.renderActivity(ev.Text); chunk != nil {
			out = append(out, *chunk)
		}
	case EventPlanUpdated:
		if chunk := r.renderActivity(formatPlanUpdate(ev.PlanExplanation, ev.Plan)); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolStarted, EventToolCompleted:
		if chunk := r.renderActivity(formatToolLifecycle(ev)); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolProgress, EventFileChangeProgress:
		if chunk := r.renderActivity(ev.Text); chunk != nil {
			out = append(out, *chunk)
		}
	case EventFileChangeStarted, EventFileChangeCompleted:
		if chunk := r.renderActivity(formatFileChangeLifecycle(ev)); chunk != nil {
			out = append(out, *chunk)
		}
	case EventToolCallDelta:
		if chunk := r.renderToolCalls(ev.ToolCalls); chunk != nil {
			out = append(out, *chunk)
		}
	}
	for _, ch := range out {
		if logEvent {
			r.logRender(ev, ch)
		}
	}
	return out
}

func (r *EventRenderer) renderText(text string) *adapteropenai.StreamChunk {
	if strings.TrimSpace(text) == "" && text == "" {
		return nil
	}
	delta := adapteropenai.StreamDelta{Content: text}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderRefusal(text string) *adapteropenai.StreamChunk {
	if strings.TrimSpace(text) == "" && text == "" {
		return nil
	}
	delta := adapteropenai.StreamDelta{Refusal: text}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderToolCalls(toolCalls []adapteropenai.ToolCall) *adapteropenai.StreamChunk {
	if len(toolCalls) == 0 {
		return nil
	}
	delta := adapteropenai.StreamDelta{ToolCalls: toolCalls}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderReasoning(ev Event) *adapteropenai.StreamChunk {
	text := strings.TrimSpace(ev.Text)
	if text == "" && ev.Text == "" {
		return nil
	}
	open := !r.reasoningOpen
	contentOut := FormatThinkingInlineDelta(open, r.decorateReasoningDelta(ev))
	r.reasoningOpen = true
	delta := adapteropenai.StreamDelta{Content: contentOut}
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

func (r *EventRenderer) renderReasoningClose() *adapteropenai.StreamChunk {
	if !r.reasoningOpen {
		return nil
	}
	r.reasoningOpen = false
	ch := r.baseChunk(adapteropenai.StreamDelta{Content: ThinkingInlineClose()})
	return &ch
}

func (r *EventRenderer) renderSyntheticReasoningPlaceholder() *adapteropenai.StreamChunk {
	if r.reasoningOpen || r.reasoningVisible {
		return nil
	}
	delta := adapteropenai.StreamDelta{Content: ThinkingInlineOpen() + ThinkingInlineClose()}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) renderActivity(text string) *adapteropenai.StreamChunk {
	text = strings.TrimSpace(text)
	if text == "" || r.reasoningOpen {
		return nil
	}
	delta := adapteropenai.StreamDelta{Content: FormatActivityDelta(text)}
	if !r.seenRole {
		delta.Role = "assistant"
		r.seenRole = true
	}
	ch := r.baseChunk(delta)
	return &ch
}

func (r *EventRenderer) baseChunk(delta adapteropenai.StreamDelta) adapteropenai.StreamChunk {
	return adapteropenai.StreamChunk{
		ID:      r.reqID,
		Object:  "chat.completion.chunk",
		Created: r.createdUnix,
		Model:   r.modelAlias,
		Choices: []adapteropenai.StreamChoice{{Index: 0, Delta: delta}},
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
	attrs := []slog.Attr{
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
	}
	attrs = append(attrs, toolCallLogAttrs(ev.ToolCalls)...)
	r.log.LogAttrs(context.Background(), slog.LevelDebug, "adapter.event.normalized", attrs...)
}

func (r *EventRenderer) logRender(ev Event, ch adapteropenai.StreamChunk) {
	delta := adapteropenai.StreamDelta{}
	if len(ch.Choices) > 0 {
		delta = ch.Choices[0].Delta
	}
	attrs := []slog.Attr{
		slog.String("component", "adapter"),
		slog.String("subcomponent", "renderer"),
		slog.String("request_id", r.reqID),
		slog.String("backend", r.backend),
		slog.String("model", r.modelAlias),
		slog.String("alias", r.modelAlias),
		slog.String("event_kind", string(ev.Kind)),
		slog.String("render_policy", renderPolicyForEvent(ev.Kind)),
		slog.Int("delta_len", len(delta.Content)+len(delta.Reasoning)+len(delta.ReasoningContent)),
	}
	attrs = append(attrs, toolCallLogAttrs(delta.ToolCalls)...)
	r.log.LogAttrs(context.Background(), slog.LevelDebug, "adapter.render.event", attrs...)
}

func shouldLogEvent(ev Event) bool {
	switch ev.Kind {
	case EventAssistantTextDelta, EventReasoningDelta:
		return false
	case EventToolCallDelta:
		return toolCallDeltaHasIdentity(ev.ToolCalls)
	default:
		return true
	}
}

func toolCallDeltaHasIdentity(toolCalls []adapteropenai.ToolCall) bool {
	for _, tc := range toolCalls {
		if strings.TrimSpace(tc.ID) != "" || strings.TrimSpace(tc.Function.Name) != "" {
			return true
		}
	}
	return false
}

func toolCallLogAttrs(toolCalls []adapteropenai.ToolCall) []slog.Attr {
	if len(toolCalls) == 0 {
		return nil
	}
	names := make([]string, 0, len(toolCalls))
	ids := make([]string, 0, len(toolCalls))
	argChars := 0
	for _, tc := range toolCalls {
		appendUniqueLogValue(&names, tc.Function.Name, 4)
		appendUniqueLogValue(&ids, tc.ID, 4)
		argChars += len(tc.Function.Arguments)
	}
	return []slog.Attr{
		slog.Int("tool_call_count", len(toolCalls)),
		slog.Any("tool_call_names", names),
		slog.Any("tool_call_ids", ids),
		slog.Int("tool_call_arg_chars", argChars),
	}
}

func appendUniqueLogValue(values *[]string, value string, limit int) {
	value = strings.TrimSpace(value)
	if value == "" || limit <= 0 || len(*values) >= limit {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}

func (r *EventRenderer) recordSuppressedEvent(ev Event) {
	if r.suppressed == nil {
		r.suppressed = make(map[EventKind]*deltaSummary)
	}
	summary := r.suppressed[ev.Kind]
	if summary == nil {
		summary = &deltaSummary{}
		r.suppressed[ev.Kind] = summary
	}
	summary.Count++
	chars := len(ev.Text)
	for _, tc := range ev.ToolCalls {
		summary.ToolCalls++
		chars += len(tc.Function.Arguments)
		summary.ToolArgChars += len(tc.Function.Arguments)
	}
	summary.Chars += chars
	if chars > summary.MaxChars {
		summary.MaxChars = chars
	}
}

func (r *EventRenderer) flushSuppressedEventSummaries() {
	if len(r.suppressed) == 0 {
		return
	}
	for _, kind := range []EventKind{EventAssistantTextDelta, EventReasoningDelta, EventToolCallDelta} {
		summary := r.suppressed[kind]
		if summary == nil || summary.Count == 0 {
			continue
		}
		r.log.LogAttrs(context.Background(), slog.LevelDebug, "adapter.event.delta_summary",
			slog.String("component", "adapter"),
			slog.String("subcomponent", "renderer"),
			slog.String("request_id", r.reqID),
			slog.String("backend", r.backend),
			slog.String("model", r.modelAlias),
			slog.String("alias", r.modelAlias),
			slog.String("event_kind", string(kind)),
			slog.Int("delta_count", summary.Count),
			slog.Int("delta_chars", summary.Chars),
			slog.Int("max_delta_chars", summary.MaxChars),
			slog.Int("tool_call_count", summary.ToolCalls),
			slog.Int("tool_call_arg_chars", summary.ToolArgChars),
		)
		delete(r.suppressed, kind)
	}
	if len(r.suppressed) == 0 {
		r.suppressed = nil
	}
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
