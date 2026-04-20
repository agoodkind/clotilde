// stream-json stdout parsing for Collect and Stream.
package fallback

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParsedError captures the error fields claude -p emits via stream-json
// when an upstream call fails (auth, rate limits, refusals). The parser
// records the latest error frame seen so callers can surface a useful
// message instead of a generic "exit status 1".
type ParsedError struct {
	Phase          string
	EventType      string
	Error          string
	IsError        bool
	APIErrorStatus int
	Result         string
}

// Message renders ParsedError as a single-line, user-facing string.
func (p ParsedError) Message() string {
	switch {
	case strings.TrimSpace(p.Result) != "":
		return strings.TrimSpace(p.Result)
	case p.APIErrorStatus != 0 && p.Error != "":
		return fmt.Sprintf("claude -p upstream %d: %s", p.APIErrorStatus, p.Error)
	case p.APIErrorStatus != 0:
		return fmt.Sprintf("claude -p upstream %d", p.APIErrorStatus)
	case p.Error != "":
		return "claude -p upstream error: " + p.Error
	default:
		return ""
	}
}

// collectStreamJSON drains stream-json from r, joins assistant text and
// reasoning, and reports usage and stop_reason from the terminal result
// frame. Non-JSON lines and JSON lines carrying an error field are
// surfaced via slog so silent CLI failures are diagnosable; the latest
// parsed error frame is also returned for caller-facing error messages.
func collectStreamJSON(r io.Reader, requestID string) (string, string, Usage, string, *ParsedError, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var textSb strings.Builder
	var reasoningSb strings.Builder
	var usage Usage
	var stopReason string
	var parsed *ParsedError
	for sc.Scan() {
		line := sc.Bytes()
		ev, ok := decodeEvent(line)
		if !ok {
			logDroppedLine(requestID, "collect", line)
			continue
		}
		if pe := parsedErrorFromEvent("collect", ev); pe != nil {
			logEventError(requestID, *pe)
			parsed = pe
		}
		switch ev.Type {
		case "assistant":
			appendAssistantDeltas(&textSb, &reasoningSb, ev, true, nil)
		case "result":
			usage = Usage{
				PromptTokens:             ev.Usage.InputTokens,
				CompletionTokens:         ev.Usage.OutputTokens,
				TotalTokens:              ev.Usage.InputTokens + ev.Usage.OutputTokens,
				CacheCreationInputTokens: ev.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     ev.Usage.CacheReadInputTokens,
			}
			stopReason = ev.StopReason
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", usage, stopReason, parsed, fmt.Errorf("fallback collect scan: %w", err)
	}
	return textSb.String(), reasoningSb.String(), usage, stopReason, parsed, nil
}

// streamStreamJSON drains stream-json from r and invokes onEvent for each
// text or reasoning fragment unless toolEnvelopeActive(req) is true, in which
// case both are buffered into fullText / fullReasoning for post-parse handling.
func streamStreamJSON(
	r io.Reader,
	req Request,
	onEvent func(StreamEvent) error,
) (fullText string, fullReasoning string, usage Usage, stopReason string, parsedErr *ParsedError, err error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var textSb strings.Builder
	var reasoningSb strings.Builder
	bufferTools := toolEnvelopeActive(req)
	for sc.Scan() {
		line := sc.Bytes()
		ev, ok := decodeEvent(line)
		if !ok {
			logDroppedLine(req.RequestID, "stream", line)
			continue
		}
		if pe := parsedErrorFromEvent("stream", ev); pe != nil {
			logEventError(req.RequestID, *pe)
			parsedErr = pe
		}
		switch ev.Type {
		case "assistant":
			if bufferTools {
				appendAssistantDeltas(&textSb, &reasoningSb, ev, true, nil)
				continue
			}
			err := appendAssistantDeltas(&textSb, &reasoningSb, ev, false, onEvent)
			if err != nil {
				return "", "", usage, stopReason, parsedErr, err
			}
		case "result":
			usage = Usage{
				PromptTokens:             ev.Usage.InputTokens,
				CompletionTokens:         ev.Usage.OutputTokens,
				TotalTokens:              ev.Usage.InputTokens + ev.Usage.OutputTokens,
				CacheCreationInputTokens: ev.Usage.CacheCreationInputTokens,
				CacheReadInputTokens:     ev.Usage.CacheReadInputTokens,
			}
			stopReason = ev.StopReason
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", usage, stopReason, parsedErr, fmt.Errorf("fallback stream scan: %w", err)
	}
	return textSb.String(), reasoningSb.String(), usage, stopReason, parsedErr, nil
}

func appendAssistantDeltas(
	textSb, reasoningSb *strings.Builder,
	ev claudeEvent,
	bufferOnly bool,
	onEvent func(StreamEvent) error,
) error {
	for _, c := range ev.Message.Content {
		switch c.Type {
		case "text":
			if c.Text == "" {
				continue
			}
			textSb.WriteString(c.Text)
			if bufferOnly || onEvent == nil {
				continue
			}
			if err := onEvent(StreamEvent{Kind: "text", Text: c.Text}); err != nil {
				return err
			}
		case "thinking":
			if c.Thinking == "" {
				continue
			}
			reasoningSb.WriteString(c.Thinking)
			if bufferOnly || onEvent == nil {
				continue
			}
			if err := onEvent(StreamEvent{Kind: "reasoning", Text: c.Thinking}); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeEvent(line []byte) (claudeEvent, bool) {
	trim := strings.TrimSpace(string(line))
	if trim == "" {
		return claudeEvent{}, false
	}
	var ev claudeEvent
	if err := json.Unmarshal([]byte(trim), &ev); err != nil {
		return claudeEvent{}, false
	}
	return ev, true
}

// droppedLineLogLimit caps the bytes of a dropped non-JSON stdout line we
// emit per slog event so a runaway CLI cannot blow up the log.
const droppedLineLogLimit = 1024

func logDroppedLine(requestID, phase string, line []byte) {
	trim := strings.TrimSpace(string(line))
	if trim == "" {
		return
	}
	preview := trim
	if len(preview) > droppedLineLogLimit {
		preview = preview[:droppedLineLogLimit]
	}
	slog.LogAttrs(context.Background(), slog.LevelDebug, "fallback.parse.dropped_line",
		slog.String("subcomponent", "fallback"),
		slog.String("request_id", requestID),
		slog.String("phase", phase),
		slog.Int("bytes", len(line)),
		slog.String("preview", preview),
	)
}

// parsedErrorFromEvent returns a ParsedError when the event carries any
// upstream-error signal, or nil otherwise. The caller decides whether to
// log it and / or surface it in the response error.
func parsedErrorFromEvent(phase string, ev claudeEvent) *ParsedError {
	if ev.Error == "" && !ev.IsError && ev.APIErrorStatus == 0 {
		return nil
	}
	return &ParsedError{
		Phase:          phase,
		EventType:      ev.Type,
		Error:          ev.Error,
		IsError:        ev.IsError,
		APIErrorStatus: ev.APIErrorStatus,
		Result:         ev.Result,
	}
}

// logEventError emits the structured fallback.event.error event for a
// single parsed error frame. Kept separate from parsing so callers can
// suppress logging in tests.
func logEventError(requestID string, pe ParsedError) {
	resultPreview := pe.Result
	if len(resultPreview) > droppedLineLogLimit {
		resultPreview = resultPreview[:droppedLineLogLimit]
	}
	slog.LogAttrs(context.Background(), slog.LevelWarn, "fallback.event.error",
		slog.String("subcomponent", "fallback"),
		slog.String("request_id", requestID),
		slog.String("phase", pe.Phase),
		slog.String("event_type", pe.EventType),
		slog.String("error", pe.Error),
		slog.Bool("is_error", pe.IsError),
		slog.Int("api_error_status", pe.APIErrorStatus),
		slog.String("result_preview", resultPreview),
	)
}
