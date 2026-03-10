package export

import (
	"bufio"
	"embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

//go:embed template.html template.css template.js vendored/marked.min.js vendored/highlight.min.js
var templateFS embed.FS

// transcriptEntry is a minimal struct for filtering JSONL lines.
type transcriptEntry struct {
	Type string `json:"type"`
}

// ExportData is the top-level structure serialized to JSON and base64-encoded.
type ExportData struct {
	SessionName string            `json:"sessionName"`
	Entries     []json.RawMessage `json:"entries"`
}

// FilterTranscript reads JSONL from r and returns only user and assistant entries as raw JSON.
func FilterTranscript(r io.Reader) ([]json.RawMessage, error) {
	scanner := bufio.NewScanner(r)
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	var entries []json.RawMessage
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var entry transcriptEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		if entry.Type == "user" || entry.Type == "assistant" {
			// Copy the line to avoid scanner buffer reuse
			raw := make(json.RawMessage, len(line))
			copy(raw, line)
			entries = append(entries, raw)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading transcript: %w", err)
	}

	if entries == nil {
		entries = []json.RawMessage{}
	}

	return entries, nil
}

// BuildHTML assembles a self-contained HTML file from the session name and filtered entries.
func BuildHTML(sessionName string, entries []json.RawMessage) (string, error) {
	if entries == nil {
		entries = []json.RawMessage{}
	}

	data := ExportData{
		SessionName: sessionName,
		Entries:     entries,
	}

	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("marshaling export data: %w", err)
	}

	b64 := base64.StdEncoding.EncodeToString(jsonBytes)

	// Read template files
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

	// Replace placeholders
	result := string(htmlTemplate)
	result = strings.Replace(result, "{{TITLE}}", sessionName, 1)
	result = strings.Replace(result, "{{CSS}}", string(css), 1)
	result = strings.Replace(result, "{{SESSION_DATA}}", b64, 1)
	result = strings.Replace(result, "{{MARKED_JS}}", string(markedJS), 1)
	result = strings.Replace(result, "{{HIGHLIGHT_JS}}", string(highlightJS), 1)
	result = strings.Replace(result, "{{JS}}", string(js), 1)

	return result, nil
}

// Export reads a transcript JSONL file, filters it, and writes a self-contained HTML file.
func Export(transcriptPath, sessionName, outputPath string) error {
	html, err := buildFromFile(transcriptPath, sessionName)
	if err != nil {
		return err
	}

	if err := os.WriteFile(outputPath, []byte(html), 0o644); err != nil {
		return fmt.Errorf("writing output file: %w", err)
	}

	return nil
}

// ExportToWriter reads a transcript JSONL file, filters it, and writes HTML to w.
func ExportToWriter(transcriptPath, sessionName string, w io.Writer) error {
	html, err := buildFromFile(transcriptPath, sessionName)
	if err != nil {
		return err
	}

	if _, err := io.WriteString(w, html); err != nil {
		return fmt.Errorf("writing HTML: %w", err)
	}

	return nil
}

func buildFromFile(transcriptPath, sessionName string) (string, error) {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return "", fmt.Errorf("opening transcript: %w", err)
	}
	defer func() { _ = f.Close() }()

	entries, err := FilterTranscript(f)
	if err != nil {
		return "", err
	}

	return BuildHTML(sessionName, entries)
}
