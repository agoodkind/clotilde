// Package content defines the adapter's provider-neutral typed boundary for
// multimodal chat and Responses message content.
package content

import (
	"encoding/json"
	"strings"
)

type Kind int

const (
	KindEmpty Kind = iota
	KindString
	KindParts
)

type PartKind string

const (
	PartText        PartKind = "text"
	PartImage       PartKind = "image"
	PartAudio       PartKind = "audio"
	PartRefusal     PartKind = "refusal"
	PartToolResult  PartKind = "tool_result"
	PartToolUse     PartKind = "tool_use"
	PartUnsupported PartKind = "unsupported"
)

type Role string

const (
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleFunction  Role = "function"
)

type Image struct {
	URL    string
	Detail string
}

type Audio struct {
	Data   string
	Format string
}

// Part is the provider-neutral adapter boundary for multimodal message content.
// Raw Content/Input fields are the narrow opaque edges required by upstream
// tool contracts; provider mappers must not inspect them without first choosing
// the concrete external contract they are translating.
type Part struct {
	Kind      PartKind
	WireType  string
	Text      string
	Image     *Image
	Audio     *Audio
	Refusal   string
	ToolUseID string
	Content   json.RawMessage
	ID        string
	Name      string
	Input     json.RawMessage
}

type wirePart struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ImageURL  *imageURL       `json:"image_url,omitempty"`
	Detail    string          `json:"detail,omitempty"`
	Audio     *wireAudio      `json:"input_audio,omitempty"`
	Refusal   string          `json:"refusal,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

type imageURL struct {
	URL    string
	Detail string
}

func (i *imageURL) UnmarshalJSON(raw []byte) error {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		i.URL = strings.TrimSpace(s)
		return nil
	}
	var obj struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return err
	}
	i.URL = strings.TrimSpace(obj.URL)
	i.Detail = strings.TrimSpace(obj.Detail)
	return nil
}

type wireAudio struct {
	Data   string `json:"data"`
	Format string `json:"format,omitempty"`
}

func NormalizeRaw(raw json.RawMessage) ([]Part, Kind) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, KindEmpty
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return []Part{{Kind: PartText, WireType: "text", Text: s}}, KindString
	}
	var parts []wirePart
	if err := json.Unmarshal(raw, &parts); err == nil {
		out := make([]Part, 0, len(parts))
		for _, part := range parts {
			out = append(out, normalizePart(part))
		}
		return out, KindParts
	}
	var single wirePart
	if err := json.Unmarshal(raw, &single); err == nil && single.Type != "" {
		return []Part{normalizePart(single)}, KindParts
	}
	return []Part{{Kind: PartText, WireType: "text", Text: string(raw)}}, KindString
}

func normalizePart(part wirePart) Part {
	wireType := strings.TrimSpace(part.Type)
	if wireType == "" {
		wireType = "text"
	}
	out := Part{
		WireType:  wireType,
		Text:      part.Text,
		Refusal:   part.Refusal,
		ToolUseID: part.ToolUseID,
		Content:   part.Content,
		ID:        part.ID,
		Name:      part.Name,
		Input:     part.Input,
	}
	switch wireType {
	case "text", "input_text", "output_text":
		out.Kind = PartText
	case "image_url", "input_image":
		out.Kind = PartImage
		if part.ImageURL != nil {
			detail := strings.TrimSpace(part.ImageURL.Detail)
			if detail == "" {
				detail = strings.TrimSpace(part.Detail)
			}
			out.Image = &Image{URL: strings.TrimSpace(part.ImageURL.URL), Detail: detail}
		}
	case "input_audio":
		out.Kind = PartAudio
		if part.Audio != nil {
			out.Audio = &Audio{Data: part.Audio.Data, Format: part.Audio.Format}
		}
	case "refusal":
		out.Kind = PartRefusal
	case "tool_result":
		out.Kind = PartToolResult
	case "tool_use":
		out.Kind = PartToolUse
	default:
		out.Kind = PartUnsupported
	}
	return out
}

// FlattenRaw is a lossy text view for logging, cache keys, summaries, and
// text-only heuristics. Provider request builders must consume NormalizeRaw
// when a backend supports multimodal content.
func FlattenRaw(raw json.RawMessage) string {
	parts, kind := NormalizeRaw(raw)
	if kind == KindString {
		if len(parts) == 0 {
			return ""
		}
		return parts[0].Text
	}
	return FlattenParts(parts)
}

func FlattenParts(parts []Part) string {
	var b strings.Builder
	for _, part := range parts {
		switch part.Kind {
		case PartText:
			b.WriteString(part.Text)
		case PartImage:
			b.WriteString("[image]")
		case PartAudio:
			b.WriteString("[audio]")
		case PartRefusal:
			b.WriteString("[refusal: ")
			b.WriteString(part.Refusal)
			b.WriteString("]")
		default:
			b.WriteString("[")
			b.WriteString(part.WireType)
			b.WriteString("]")
		}
	}
	return b.String()
}
