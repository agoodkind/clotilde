package export

import (
	"bufio"
	"bytes"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"

	"github.com/fgrehm/clotilde/internal/transcript"
)

//go:embed template.html template.css template.js vendored/marked.min.js vendored/highlight.min.js
var templateFS embed.FS

// ExportMessage is the structured format serialized to JSON for the HTML template.
type ExportMessage struct {
	Role      string           `json:"role"`
	Timestamp string           `json:"timestamp"`
	Text      string           `json:"text"`
	Thinking  string           `json:"thinking,omitempty"`
	Tools     []ExportToolCall `json:"tools,omitempty"`
}

// ExportToolCall is a tool call serialized for the HTML template.
type ExportToolCall struct {
	Name    string         `json:"name"`
	Input   map[string]any `json:"input,omitempty"`
	Output  string         `json:"output,omitempty"`
	IsError bool           `json:"isError,omitempty"`
}

// ExportData is the top-level structure serialized to JSON and base64-encoded.
type ExportData struct {
	SessionName string          `json:"sessionName"`
	Messages    []ExportMessage `json:"messages"`
}

// FilterTranscript reads JSONL from r and returns only user and assistant entries as raw JSON.
// Preserved for backward compatibility — callers that need raw JSON can use this.
func FilterTranscript(r io.Reader) ([]json.RawMessage, error) {
	reader := bufio.NewReader(r)

	var entries []json.RawMessage
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimRight(line, "\r\n")

			if len(line) > 0 {
				var entry struct {
					Type string `json:"type"`
				}
				if jsonErr := json.Unmarshal(line, &entry); jsonErr == nil {
					if entry.Type == "user" || entry.Type == "assistant" {
						raw := make(json.RawMessage, len(line))
						copy(raw, line)
						entries = append(entries, raw)
					}
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading transcript: %w", err)
		}
	}

	if entries == nil {
		entries = []json.RawMessage{}
	}

	return entries, nil
}

// BuildHTMLFromMessages builds a self-contained HTML file from structured messages.
func BuildHTMLFromMessages(sessionName string, messages []transcript.Message) (string, error) {
	exportMessages := make([]ExportMessage, 0, len(messages))
	for _, m := range messages {
		em := ExportMessage{
			Role:      m.Role,
			Timestamp: m.Timestamp.Format("2006-01-02T15:04:05Z"),
			Text:      m.Text,
			Thinking:  m.Thinking,
		}
		for _, t := range m.Tools {
			em.Tools = append(em.Tools, ExportToolCall{
				Name:    t.Name,
				Input:   t.Input,
				Output:  t.Output,
				IsError: t.IsError,
			})
		}
		exportMessages = append(exportMessages, em)
	}

	data := ExportData{
		SessionName: sessionName,
		Messages:    exportMessages,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshaling export data: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(jsonBytes)
	return buildHTMLWithData(sessionName, b64)
}

// BuildHTML assembles a self-contained HTML file from the session name and filtered entries.
// Preserved for backward compatibility with raw JSON entries.
func BuildHTML(sessionName string, entries []json.RawMessage) (string, error) {
	if entries == nil {
		entries = []json.RawMessage{}
	}

	type legacyData struct {
		SessionName string            `json:"sessionName"`
		Entries     []json.RawMessage `json:"entries"`
	}

	data := legacyData{
		SessionName: sessionName,
		Entries:     entries,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshaling export data: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(jsonBytes)
	return buildHTMLWithData(sessionName, b64)
}

func buildHTMLWithData(sessionName, b64Data string) (string, error) {
	htmlTemplate, err := templateFS.ReadFile("template.html")
	if err != nil {
		return "", fmt.Errorf("reading template.html: %w", err)
	}
	css, err := templateFS.ReadFile("template.css")
	if err != nil {
		return "", fmt.Errorf("reading template.css: %w", err)
	}
	js, err := templateFS.ReadFile("template.js")
	if err != nil {
		return "", fmt.Errorf("reading template.js: %w", err)
	}
	markedJS, err := templateFS.ReadFile("vendored/marked.min.js")
	if err != nil {
		return "", fmt.Errorf("reading marked.min.js: %w", err)
	}
	highlightJS, err := templateFS.ReadFile("vendored/highlight.min.js")
	if err != nil {
		return "", fmt.Errorf("reading highlight.min.js: %w", err)
	}

	r := strings.NewReplacer(
		"{{TITLE}}", html.EscapeString(sessionName),
		"{{CSS}}", string(css),
		"{{SESSION_DATA}}", b64Data,
		"{{MARKED_JS}}", string(markedJS),
		"{{HIGHLIGHT_JS}}", string(highlightJS),
		"{{JS}}", string(js),
	)

	return r.Replace(string(htmlTemplate)), nil
}
