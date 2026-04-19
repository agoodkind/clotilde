package compact

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ToolDetail enumerates the three rendering levels for tool pairs.
// Highest fidelity first; the target loop demotes oldest pairs one
// level at a time until the budget fits.
type ToolDetail int

const (
	// ToolDetailFull renders the call line plus a fenced code block
	// with the full result body (head/tail truncated at TruncTokens).
	ToolDetailFull ToolDetail = iota
	// ToolDetailLineOnly renders just the "Tool(args) -> N lines (T tok)"
	// line; the body block is omitted.
	ToolDetailLineOnly
	// ToolDetailDrop omits the pair entirely from the synthetic output.
	ToolDetailDrop
)

// SynthOptions controls how the post-boundary slice is rendered into
// the synthetic content array. Defaults are highest fidelity: keep
// images as image blocks, render every tool pair full, include every
// chat turn verbatim, and keep thinking blocks out (they never carry
// across turn boundaries in the live API anyway).
type SynthOptions struct {
	// DropThinking, if true, drops thinking and redacted_thinking
	// content. Default false; the orchestrator turns this on
	// unconditionally when running a target loop because thinking
	// content does not survive turn boundaries in the live API.
	DropThinking bool

	// ImagesAsPlaceholder, if true, replaces image blocks with a
	// `[image: media_type, N bytes]` text marker instead of carrying
	// the base64 data forward.
	ImagesAsPlaceholder bool

	// ToolDefault is the rendering level applied to every tool pair
	// that does not appear in ToolDetailOverride.
	ToolDefault ToolDetail

	// ToolDetailOverride lets the orchestrator demote specific (oldest)
	// tool_use ids without changing the default. The map is keyed by
	// tool_use id.
	ToolDetailOverride map[string]ToolDetail

	// DroppedChatEntries lists post-boundary entry indexes (into
	// Slice.PostBoundary) that should be omitted from the chat
	// transcript. The orchestrator populates this oldest-first while
	// always preserving the most recent assistant turn plus its
	// preceding user turn.
	DroppedChatEntries map[int]bool

	// TruncTokens caps tool result body length when ToolDefault is
	// Full. Approximated as 4 chars per token. Default 4000.
	TruncTokens int
}

// OutputBlock is one element of the synthetic content array. It is a
// closed sum: either a text block (Text set, Image nil) or an image
// block (Image set, Text empty). Marshaling routes through
// MarshalJSON so the on-disk shape matches Anthropic's content-array
// schema exactly.
type OutputBlock struct {
	Text  string
	Image *OutputImage
}

// OutputImage is the typed payload of an image block. Mirrors the
// Anthropic image source object: base64-encoded data plus media type.
type OutputImage struct {
	MediaType string
	DataB64   string
}

// MarshalJSON serializes an OutputBlock as either
// {"type":"text","text":"..."} or {"type":"image","source":{...}}.
func (b OutputBlock) MarshalJSON() ([]byte, error) {
	if b.Image != nil {
		payload := struct {
			Type   string `json:"type"`
			Source struct {
				Type      string `json:"type"`
				MediaType string `json:"media_type"`
				Data      string `json:"data"`
			} `json:"source"`
		}{Type: "image"}
		payload.Source.Type = "base64"
		payload.Source.MediaType = b.Image.MediaType
		payload.Source.Data = b.Image.DataB64
		return json.Marshal(payload)
	}
	payload := struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: b.Text}
	return json.Marshal(payload)
}

// Synthesize builds the content array for the post-boundary user
// message. Output blocks are ordered: header, chat transcript, tool
// activity section, footer. Image blocks (when not placeholders) are
// inserted in the chat stream where they originally appeared so the
// model still sees them inline.
func Synthesize(slice *Slice, opts SynthOptions) []OutputBlock {
	if opts.TruncTokens <= 0 {
		opts.TruncTokens = 4000
	}
	out := []OutputBlock{}
	out = append(out, textBlock("## Continued from prior session (transcript below)\n\n"))
	out = append(out, renderChat(slice, opts)...)
	if toolBlocks := renderToolActivity(slice, opts); len(toolBlocks) > 0 {
		out = append(out, toolBlocks...)
	}
	out = append(out, textBlock("## Continue from here.\n"))
	return out
}

// renderChat walks PostBoundary in order and emits a text block per
// chat turn, plus image blocks (or image placeholders) inline. Tool
// activity is excluded; that lives in its own section below.
func renderChat(slice *Slice, opts SynthOptions) []OutputBlock {
	var out []OutputBlock
	for ei, e := range slice.PostBoundary {
		if opts.DroppedChatEntries != nil && opts.DroppedChatEntries[ei] {
			continue
		}
		switch e.Type {
		case "user":
			if isToolResultOnly(e) {
				// Tool results render in the tool activity section.
				continue
			}
			text := chatTextFrom(e, opts)
			if text == "" && !hasRenderableImage(e, opts) {
				continue
			}
			label := turnLabel("User", e.Timestamp)
			if text != "" {
				out = append(out, textBlock(label+text+"\n\n"))
			} else {
				out = append(out, textBlock(label+"\n\n"))
			}
			out = append(out, renderInlineImages(e, opts)...)
		case "assistant":
			text := chatTextFrom(e, opts)
			if text == "" && !hasRenderableImage(e, opts) {
				continue
			}
			label := turnLabel("Assistant", e.Timestamp)
			if text != "" {
				out = append(out, textBlock(label+text+"\n\n"))
			}
			out = append(out, renderInlineImages(e, opts)...)
		}
	}
	return out
}

// renderToolActivity emits one trailing section summarising tool
// invocations in the order they appeared.
func renderToolActivity(slice *Slice, opts SynthOptions) []OutputBlock {
	type pairRender struct {
		ei     int
		bi     int
		useID  string
		entry  Entry
		block  ContentBlock
		detail ToolDetail
	}
	var pairs []pairRender
	for ei, e := range slice.PostBoundary {
		if e.Type != "assistant" {
			continue
		}
		for bi, b := range e.Content {
			if b.Type != "tool_use" {
				continue
			}
			detail := opts.ToolDefault
			if override, ok := opts.ToolDetailOverride[b.ToolUseID]; ok {
				detail = override
			}
			if detail == ToolDetailDrop {
				continue
			}
			pairs = append(pairs, pairRender{
				ei: ei, bi: bi, useID: b.ToolUseID, entry: e, block: b, detail: detail,
			})
		}
	}
	if len(pairs) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString("## Tool activity\n\n")
	for _, p := range pairs {
		argSummary := summarizeToolArgs(p.block)
		var resultBlock *ContentBlock
		if pair, ok := slice.PairIndex[p.useID]; ok && pair.ResultEntryIdx >= 0 {
			rEntry := slice.PostBoundary[pair.ResultEntryIdx]
			if pair.ResultBlockIdx < len(rEntry.Content) {
				rb := rEntry.Content[pair.ResultBlockIdx]
				resultBlock = &rb
			}
		}
		resultText, lines, tokens := flattenToolResult(resultBlock)
		fmt.Fprintf(&sb, "- %s(%s) -> %d lines (~%d tok)", p.block.ToolName, argSummary, lines, tokens)
		if resultBlock != nil && resultBlock.ToolIsError {
			sb.WriteString(" [error]")
		}
		sb.WriteString("\n")
		if p.detail == ToolDetailFull && resultText != "" {
			truncated := truncHeadTail(resultText, opts.TruncTokens*4)
			sb.WriteString("```text\n")
			sb.WriteString(truncated)
			if !strings.HasSuffix(truncated, "\n") {
				sb.WriteString("\n")
			}
			sb.WriteString("```\n")
		}
		sb.WriteString("\n")
	}
	return []OutputBlock{textBlock(sb.String())}
}

// chatTextFrom concatenates text blocks from one entry, optionally
// dropping thinking content (which we never carry across boundaries).
func chatTextFrom(e Entry, opts SynthOptions) string {
	var sb strings.Builder
	if e.TextOnly != "" {
		sb.WriteString(e.TextOnly)
	}
	for _, b := range e.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(b.Text)
			}
		case "thinking", "redacted_thinking":
			if !opts.DropThinking && b.Thinking != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString("[thinking] " + b.Thinking)
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

// renderInlineImages emits image blocks (or placeholders) for one
// entry. Placed AFTER the entry's text label so the visual flow is
// "User: ... <image> Assistant: ...".
func renderInlineImages(e Entry, opts SynthOptions) []OutputBlock {
	var out []OutputBlock
	for _, b := range e.Content {
		if b.Type != "image" {
			continue
		}
		if opts.ImagesAsPlaceholder {
			placeholder := fmt.Sprintf("[image: %s, ~%d bytes]\n\n", b.ImageMediaType, b.ImageBytes)
			out = append(out, textBlock(placeholder))
			continue
		}
		if b.ImageDataB64 == "" {
			continue
		}
		out = append(out, OutputBlock{
			Image: &OutputImage{
				MediaType: b.ImageMediaType,
				DataB64:   b.ImageDataB64,
			},
		})
	}
	return out
}

// hasRenderableImage tells the chat renderer whether to still emit a
// turn label even when text is empty (because we are about to emit an
// image block).
func hasRenderableImage(e Entry, opts SynthOptions) bool {
	for _, b := range e.Content {
		if b.Type != "image" {
			continue
		}
		if opts.ImagesAsPlaceholder || b.ImageDataB64 != "" {
			return true
		}
	}
	return false
}

// isToolResultOnly reports whether a user entry consists entirely of
// tool_result blocks (no text from the actual user). Such entries are
// excluded from the chat stream because they belong in the tool
// activity section.
func isToolResultOnly(e Entry) bool {
	if e.TextOnly != "" {
		return false
	}
	if len(e.Content) == 0 {
		return false
	}
	for _, b := range e.Content {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// summarizeToolArgs renders a short, human-readable view of a
// tool_use input map. Long string values are truncated; nested maps
// collapse to "{...}" so the line stays readable.
func summarizeToolArgs(b ContentBlock) string {
	if len(b.ToolInput) == 0 {
		return ""
	}
	keys := make([]string, 0, len(b.ToolInput))
	for k := range b.ToolInput {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, summarizeArgValue(b.ToolInput[k])))
	}
	return strings.Join(parts, ", ")
}

func summarizeArgValue(v any) string {
	switch x := v.(type) {
	case string:
		if len(x) > 80 {
			return fmt.Sprintf("%q...", x[:77])
		}
		return fmt.Sprintf("%q", x)
	case bool, float64, int, int64:
		return fmt.Sprintf("%v", x)
	case []any:
		return fmt.Sprintf("[%d items]", len(x))
	case map[string]any:
		return "{...}"
	default:
		return fmt.Sprintf("%v", x)
	}
}

// flattenToolResult concatenates a tool_result.content array into one
// text body and counts visible lines and rough token count.
func flattenToolResult(b *ContentBlock) (string, int, int) {
	if b == nil {
		return "", 0, 0
	}
	var sb strings.Builder
	for _, sub := range b.ToolContent {
		if sub.Type == "text" && sub.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(sub.Text)
		}
	}
	body := sb.String()
	if body == "" {
		return "", 0, 0
	}
	lines := strings.Count(body, "\n") + 1
	tokens := len(body) / 4
	return body, lines, tokens
}

// truncHeadTail keeps the first and last halves of body so the model
// sees both ends. Marker line announces the elision.
func truncHeadTail(body string, maxBytes int) string {
	if maxBytes <= 0 || len(body) <= maxBytes {
		return body
	}
	half := maxBytes / 2
	head := body[:half]
	tail := body[len(body)-half:]
	dropped := len(body) - len(head) - len(tail)
	return head + "\n\n... [elided " + fmt.Sprintf("%d", dropped) + " bytes] ...\n\n" + tail
}

func turnLabel(role string, ts time.Time) string {
	if ts.IsZero() {
		return fmt.Sprintf("**%s:** ", role)
	}
	return fmt.Sprintf("**%s (%s):** ", role, ts.UTC().Format("2006-01-02 15:04Z"))
}

func textBlock(s string) OutputBlock {
	return OutputBlock{Text: s}
}
