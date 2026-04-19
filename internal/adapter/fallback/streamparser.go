// stream-json stdout parsing for Collect and Stream.
package fallback

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// collectStreamJSON drains stream-json from r, joins assistant text and
// reasoning, and reports usage and stop_reason from the terminal result frame.
func collectStreamJSON(r io.Reader) (string, string, Usage, string, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var textSb strings.Builder
	var reasoningSb strings.Builder
	var usage Usage
	var stopReason string
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			appendAssistantDeltas(&textSb, &reasoningSb, ev, true, nil)
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
			stopReason = ev.StopReason
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", usage, stopReason, fmt.Errorf("fallback collect scan: %w", err)
	}
	return textSb.String(), reasoningSb.String(), usage, stopReason, nil
}

// streamStreamJSON drains stream-json from r and invokes onEvent for each
// text or reasoning fragment unless toolEnvelopeActive(req) is true, in which
// case both are buffered into fullText / fullReasoning for post-parse handling.
func streamStreamJSON(
	r io.Reader,
	req Request,
	onEvent func(StreamEvent) error,
) (fullText string, fullReasoning string, usage Usage, stopReason string, err error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var textSb strings.Builder
	var reasoningSb strings.Builder
	bufferTools := toolEnvelopeActive(req)
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			if bufferTools {
				appendAssistantDeltas(&textSb, &reasoningSb, ev, true, nil)
				continue
			}
			err := appendAssistantDeltas(&textSb, &reasoningSb, ev, false, onEvent)
			if err != nil {
				return "", "", usage, stopReason, err
			}
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
			stopReason = ev.StopReason
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", usage, stopReason, fmt.Errorf("fallback stream scan: %w", err)
	}
	return textSb.String(), reasoningSb.String(), usage, stopReason, nil
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
