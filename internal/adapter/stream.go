package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"goodkind.io/clyde/internal/adapter/finishreason"
)

// claudeEvent is one line of claude -p --output-format stream-json.
// Only the fields the adapter uses are decoded; everything else is
// tolerated so future claude additions do not break parsing.
type claudeEvent struct {
	Type         string        `json:"type"`
	Subtype      string        `json:"subtype,omitempty"`
	Message      claudeMessage `json:"message,omitempty"`
	TotalCostUSD float64       `json:"total_cost_usd,omitempty"`
	Usage        claudeUsage   `json:"usage,omitempty"`
	StopReason   string        `json:"stop_reason,omitempty"`
}

type claudeMessage struct {
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

type claudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// usageFromClaudeResult builds an OpenAI Usage from a claude -p result
// event's usage block. When the upstream reports any cache_read tokens
// they're surfaced via the OpenAI-canonical
// prompt_tokens_details.cached_tokens field so clients see cache
// efficiency without having to read the slog JSONL.
func usageFromClaudeResult(u claudeUsage) Usage {
	out := Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens,
	}
	if u.CacheReadInputTokens > 0 {
		out.PromptTokensDetails = &PromptTokensDetails{CachedTokens: u.CacheReadInputTokens}
	}
	return out
}

// StreamSink receives one translated chunk at a time. The HTTP
// handler passes a function that writes the SSE frame. The
// translator also emits a synthetic terminal chunk.
type StreamSink func(chunk StreamChunk) error

// TranslateStream reads newline delimited claude stream-json from r
// and forwards OpenAI style chunks into sink. It returns the final
// usage counts so the caller can emit a closing usage frame when the
// client requested it, and the mapped finish_reason for logging.
// TranslateStream does not write [DONE]; that
// is the HTTP layer's job because only it owns the writer.
func TranslateStream(r io.Reader, modelAlias, completionID string, sink StreamSink) (Usage, string, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)

	firstText := true
	var usage Usage
	created := time.Now().Unix()
	var apiStopReason string

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev claudeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					if c.Text == "" {
						continue
					}
					chunk := StreamChunk{
						ID:      completionID,
						Object:  "chat.completion.chunk",
						Created: created,
						Model:   modelAlias,
						Choices: []StreamChoice{{
							Index: 0,
							Delta: StreamDelta{Content: c.Text},
						}},
					}
					if firstText {
						chunk.Choices[0].Delta.Role = "assistant"
						firstText = false
					}
					if err := sink(chunk); err != nil {
						return usage, "", err
					}
				case "thinking":
					if c.Thinking == "" {
						continue
					}
					chunk := StreamChunk{
						ID:      completionID,
						Object:  "chat.completion.chunk",
						Created: created,
						Model:   modelAlias,
						Choices: []StreamChoice{{
							Index: 0,
							Delta: StreamDelta{ReasoningContent: c.Thinking},
						}},
					}
					if firstText {
						chunk.Choices[0].Delta.Role = "assistant"
						firstText = false
					}
					if err := sink(chunk); err != nil {
						return usage, "", err
					}
				}
			}
		case "result":
			usage = usageFromClaudeResult(ev.Usage)
			apiStopReason = ev.StopReason
		}
	}
	if err := sc.Err(); err != nil {
		return usage, "", fmt.Errorf("stream scan: %w", err)
	}

	fr := finishreason.FromAnthropicNonStream(apiStopReason)
	if fr == "" {
		fr = "stop"
	}
	sink(StreamChunk{
		ID:      completionID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelAlias,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &fr,
		}},
	})

	return usage, fr, nil
}

// CollectStream is the non streaming path. It drains the claude
// output, joins every assistant text chunk, and returns the
// accumulated message plus the usage counts.
func CollectStream(r io.Reader) (string, Usage, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)

	var sb strings.Builder
	var usage Usage

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev claudeEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
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
			usage = usageFromClaudeResult(ev.Usage)
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), usage, fmt.Errorf("collect scan: %w", err)
	}
	return sb.String(), usage, nil
}
