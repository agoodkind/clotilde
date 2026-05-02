package daemon

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/session"
)

func TestCompressExportWhitespaceTidyPreservesStructure(t *testing.T) {
	in := "  This   is   prose.  \n\n\n- keep   list spacing\n\n```python\nif x:\n    print(x)\n```\n"
	got := compressExportWhitespaceText(in, clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_TIDY)
	want := "This is prose.\n\n- keep   list spacing\n\n```python\nif x:\n    print(x)\n```"
	if got != want {
		t.Fatalf("compressed text = %q want %q", got, want)
	}
}

func TestCompressExportWhitespaceDenseDropsBlankLines(t *testing.T) {
	in := "First   line\n\n\nSecond line\n"
	got := compressExportWhitespaceText(in, clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_DENSE)
	want := "First line\nSecond line"
	if got != want {
		t.Fatalf("dense text = %q want %q", got, want)
	}
}

func TestCompressExportWhitespaceCompactsJSON(t *testing.T) {
	got := string(compressExportWhitespace(
		[]byte("[\n  {\"role\": \"user\"}\n]\n"),
		clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_JSON,
		clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_COMPACT,
	))
	want := `[{"role":"user"}]`
	if got != want {
		t.Fatalf("json = %q want %q", got, want)
	}
}

func TestBuildSessionExportAppliesContentToggles(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join([]string{
		`{"type":"user","timestamp":"2026-04-29T00:00:00Z","message":{"role":"user","content":"hello user"}}`,
		`{"type":"assistant","timestamp":"2026-04-29T00:00:01Z","message":{"role":"assistant","content":[{"type":"thinking","thinking":"private chain"},{"type":"tool_use","id":"toolu_1","name":"Read","input":{"file_path":"AGENTS.md"}},{"type":"text","text":"assistant text"}]}}`,
		`{"type":"user","timestamp":"2026-04-29T00:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"tool output text","is_error":false}]}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(transcriptPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	sess := session.NewSession("demo", "session-id")
	sess.Metadata.SetProviderTranscriptPath(transcriptPath)

	exported, err := buildSessionExport(sess, &clydev1.ExportSessionRequest{
		SessionName:            "demo",
		Format:                 clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_MARKDOWN,
		HistoryStart:           1 << 30,
		WhitespaceCompression:  clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_PRESERVE,
		IncludeChat:            false,
		IncludeThinking:        true,
		IncludeToolCalls:       true,
		IncludeToolOutputs:     true,
		IncludeRawJsonMetadata: false,
	})
	if err != nil {
		t.Fatalf("build export: %v", err)
	}
	text := string(exported)
	for _, want := range []string{"private chain", "[tool: Read]", "tool output text"} {
		if !strings.Contains(text, want) {
			t.Fatalf("export missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"hello user", "assistant text"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("export included chat text %q despite IncludeChat=false:\n%s", notWant, text)
		}
	}
}

func TestBuildSessionExportCanIncludeSystemPrompts(t *testing.T) {
	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	body := `{"type":"user","timestamp":"2026-04-29T00:00:00Z","message":{"role":"user","content":"<system-reminder>keep this system prompt</system-reminder>\nhello"}}` + "\n"
	if err := os.WriteFile(transcriptPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	sess := session.NewSession("demo", "session-id")
	sess.Metadata.SetProviderTranscriptPath(transcriptPath)

	without, err := buildSessionExport(sess, &clydev1.ExportSessionRequest{
		SessionName:      "demo",
		Format:           clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_MARKDOWN,
		HistoryStart:     1 << 30,
		IncludeChat:      true,
		IncludeToolCalls: false,
	})
	if err != nil {
		t.Fatalf("build export without system: %v", err)
	}
	if strings.Contains(string(without), "keep this system prompt") {
		t.Fatalf("export included system prompt with IncludeSystemPrompts=false:\n%s", string(without))
	}

	with, err := buildSessionExport(sess, &clydev1.ExportSessionRequest{
		SessionName:          "demo",
		Format:               clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_MARKDOWN,
		HistoryStart:         1 << 30,
		IncludeChat:          true,
		IncludeSystemPrompts: true,
		IncludeToolCalls:     false,
	})
	if err != nil {
		t.Fatalf("build export with system: %v", err)
	}
	if !strings.Contains(string(with), "keep this system prompt") {
		t.Fatalf("export missing system prompt with IncludeSystemPrompts=true:\n%s", string(with))
	}
}
