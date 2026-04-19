// stream-json stdout parsing for Collect and Stream.
package fallback

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// collectStreamJSON drains stream-json from r, joins assistant text,
// and reports the usage counts from the terminal `result` frame.
func collectStreamJSON(r io.Reader) (string, Usage, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var sb strings.Builder
	var usage Usage
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), usage, fmt.Errorf("fallback collect scan: %w", err)
	}
	return sb.String(), usage, nil
}

// streamStreamJSON drains stream-json from r and invokes onDelta
// with each assistant text chunk unless toolEnvelopeActive(req) is
// true, in which case text is joined into fullText for post-parse
// envelope handling.
func streamStreamJSON(r io.Reader, req Request, onDelta func(string) error) (fullText string, usage Usage, err error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var sb strings.Builder
	bufferTools := toolEnvelopeActive(req)
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "text" || c.Text == "" {
					continue
				}
				if bufferTools {
					sb.WriteString(c.Text)
					continue
				}
				if err := onDelta(c.Text); err != nil {
					return "", usage, err
				}
			}
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", usage, fmt.Errorf("fallback stream scan: %w", err)
	}
	return sb.String(), usage, nil
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
