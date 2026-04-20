package fallback

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeClaudeScript is a minimal shell script that emits valid
// `claude -p --output-format stream-json` output and exits 0.
// The driver should parse the assistant text and the result usage
// frame the same way it would for the real CLI.
const fakeClaudeScript = `#!/usr/bin/env sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"hello "}]}}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"world"}]}}'
echo '{"type":"result","usage":{"input_tokens":11,"output_tokens":2}}'
`

// fakeClaudeFailScript emits no assistant text and exits non-zero.
const fakeClaudeFailScript = `#!/usr/bin/env sh
echo "boom" 1>&2
exit 7
`

// fakeClaudeCacheScript emits a result frame with cache accounting so
// tests can assert the parser propagates cache tokens through Usage.
const fakeClaudeCacheScript = `#!/usr/bin/env sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
echo '{"type":"result","usage":{"input_tokens":100,"output_tokens":5,"cache_creation_input_tokens":800,"cache_read_input_tokens":4000}}'
`

func writeFakeBinary(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-binary tests use a POSIX shebang; not portable to windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}
	return path
}

func TestConfigValidate(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"empty binary", Config{Timeout: time.Second, ScratchDir: "/tmp"}, false},
		{"zero timeout", Config{Binary: "/bin/echo", ScratchDir: "/tmp"}, false},
		{"empty scratch", Config{Binary: "/bin/echo", Timeout: time.Second}, false},
		{"all set", Config{Binary: "/bin/echo", Timeout: time.Second, ScratchDir: "/tmp"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want nil, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatalf("want error, got nil")
			}
		})
	}
}

func TestEnsureScratchDirCreates(t *testing.T) {
	base := t.TempDir()
	dir, err := EnsureScratchDir(base, "fallback")
	if err != nil {
		t.Fatalf("EnsureScratchDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("want dir, got %v", info.Mode())
	}
}

func TestEnsureScratchDirRejectsEmpty(t *testing.T) {
	if _, err := EnsureScratchDir("", "x"); err == nil {
		t.Fatalf("want error for empty base")
	}
	if _, err := EnsureScratchDir("/tmp", ""); err == nil {
		t.Fatalf("want error for empty subdir")
	}
}

func TestCollectHappyPath(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeScript)
	scratch := t.TempDir()
	c := New(Config{
		Binary:     bin,
		Timeout:    5 * time.Second,
		ScratchDir: scratch,
	})
	res, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Text != "hello world" {
		t.Fatalf("text = %q want %q", res.Text, "hello world")
	}
	if res.Usage.PromptTokens != 11 || res.Usage.CompletionTokens != 2 || res.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.Stop != "stop" {
		t.Fatalf("stop = %q want stop", res.Stop)
	}
}

func TestCollectPropagatesCacheTokens(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeCacheScript)
	scratch := t.TempDir()
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: scratch})
	res, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Usage.CacheCreationInputTokens != 800 {
		t.Fatalf("cache_creation = %d want 800", res.Usage.CacheCreationInputTokens)
	}
	if res.Usage.CacheReadInputTokens != 4000 {
		t.Fatalf("cache_read = %d want 4000", res.Usage.CacheReadInputTokens)
	}
}

func TestCollectRejectsEmptyModel(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeScript)
	scratch := t.TempDir()
	c := New(Config{Binary: bin, Timeout: time.Second, ScratchDir: scratch})
	_, err := c.Collect(context.Background(), Request{Model: ""})
	if err == nil {
		t.Fatalf("want error for empty model")
	}
	if !strings.Contains(err.Error(), "Model") {
		t.Fatalf("want error mentioning Model, got %v", err)
	}
}

func TestCollectSurfacesNonZeroExit(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeFailScript)
	scratch := t.TempDir()
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: scratch})
	_, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("want error from non-zero exit")
	}
	if !strings.Contains(err.Error(), "claude -p exited") {
		t.Fatalf("want exit error, got %v", err)
	}
}

func TestCollectTimesOut(t *testing.T) {
	// A binary that sleeps longer than the timeout. Use /bin/sleep.
	if _, err := os.Stat("/bin/sleep"); err != nil {
		t.Skip("/bin/sleep not available")
	}
	scratch := t.TempDir()
	c := New(Config{
		Binary:     "/bin/sleep",
		Timeout:    150 * time.Millisecond,
		ScratchDir: scratch,
	})
	// /bin/sleep ignores the args we pass, but we still need a
	// non-empty Model to pass the spawn precondition. The
	// subprocess will be killed by the deadline before it produces
	// any stream-json, so the parser drains an empty stdout and
	// the wait surfaces the killed exit.
	_, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err == nil {
		t.Fatalf("want timeout error")
	}
}

func TestStreamHappyPath(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeScript)
	scratch := t.TempDir()
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: scratch})
	var deltas []string
	sr, err := c.Stream(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, func(ev StreamEvent) error {
		if ev.Kind == "text" {
			deltas = append(deltas, ev.Text)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got := strings.Join(deltas, ""); got != "hello world" {
		t.Fatalf("deltas = %q want %q", got, "hello world")
	}
	if sr.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %+v", sr.Usage)
	}
	if sr.Stop != "stop" {
		t.Fatalf("stop = %q", sr.Stop)
	}
}

func TestSuppressHookEnvSetsBothVars(t *testing.T) {
	// A fake binary that emits its env values inside the
	// assistant text frame. Using printf with explicit %s avoids
	// the surprise tokenization sh's `echo` does with adjacent
	// quoted strings.
	body := `#!/usr/bin/env sh
printf '{"type":"assistant","message":{"content":[{"type":"text","text":"DD=%s SH=%s"}]}}\n' "${CLYDE_DISABLE_DAEMON:-unset}" "${CLYDE_SUPPRESS_HOOKS:-unset}"
printf '{"type":"result","usage":{"input_tokens":1,"output_tokens":1}}\n'
`
	bin := writeFakeBinary(t, body)
	scratch := t.TempDir()
	c := New(Config{
		Binary:          bin,
		Timeout:         5 * time.Second,
		ScratchDir:      scratch,
		SuppressHookEnv: true,
	})
	res, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Text != "DD=1 SH=1" {
		t.Fatalf("env not propagated; got text %q want %q", res.Text, "DD=1 SH=1")
	}
}

func TestSuppressHookEnvUnsetWhenFalse(t *testing.T) {
	body := `#!/usr/bin/env sh
printf '{"type":"assistant","message":{"content":[{"type":"text","text":"DD=%s"}]}}\n' "${CLYDE_DISABLE_DAEMON:-unset}"
printf '{"type":"result","usage":{"input_tokens":1,"output_tokens":1}}\n'
`
	bin := writeFakeBinary(t, body)
	scratch := t.TempDir()
	c := New(Config{
		Binary:     bin,
		Timeout:    5 * time.Second,
		ScratchDir: scratch,
	})
	res, err := c.Collect(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if res.Text != "DD=unset" {
		t.Fatalf("env should be unset; got %q want %q", res.Text, "DD=unset")
	}
}

func TestRenderPromptSkipsSystem(t *testing.T) {
	got := renderPrompt([]Message{
		{Role: "system", Content: "ignored"},
		{Role: "developer", Content: "also ignored"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello"},
		{Role: "user", Content: "what's up"},
	})
	want := "user: hi\n\nassistant: hello\n\nuser: what's up"
	if got != want {
		t.Fatalf("renderPrompt = %q want %q", got, want)
	}
}

func TestDecodeEventTolerantOfGarbage(t *testing.T) {
	// Garbage lines are skipped silently so future stream-json
	// extensions don't break us.
	if _, ok := decodeEvent([]byte("not json")); ok {
		t.Fatalf("want decode failure")
	}
	if _, ok := decodeEvent([]byte("")); ok {
		t.Fatalf("want decode failure on empty")
	}
	ev, ok := decodeEvent([]byte(`{"type":"assistant"}`))
	if !ok {
		t.Fatalf("want decode success")
	}
	if ev.Type != "assistant" {
		t.Fatalf("type = %q", ev.Type)
	}
}

// sanity: New + Validate is a no-op contract; New does not panic
// even when the parent forgot to validate.
func TestNewDoesNotPanicOnPartialConfig(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("New panicked: %v", r)
		}
	}()
	c := New(Config{})
	if c == nil {
		t.Fatalf("New returned nil")
	}
}

// sanity: Validate's error messages are stable enough that callers
// can substring-match them. This pins the prefix that
// buildFallbackConfig wraps.
func TestValidateMentionsField(t *testing.T) {
	err := Config{}.Validate()
	if err == nil {
		t.Fatalf("want error")
	}
	if !errors.Is(err, err) || !strings.Contains(err.Error(), "Binary") {
		t.Fatalf("want mention of Binary, got %v", err)
	}
}

func Test_parse_envelope_simple(t *testing.T) {
	t.Run("parse_envelope_simple", func(t *testing.T) {
		raw := `{"tool_calls":[{"name":"x","arguments":{"a":1}}]}`
		calls, prefix, ok := parseToolEnvelope(raw)
		if !ok || prefix != "" {
			t.Fatalf("ok=%v prefix=%q", ok, prefix)
		}
		if len(calls) != 1 || calls[0].Name != "x" || calls[0].Arguments != `{"a":1}` {
			t.Fatalf("calls=%+v", calls)
		}
	})
}

func Test_parse_envelope_multi(t *testing.T) {
	t.Run("parse_envelope_multi", func(t *testing.T) {
		raw := `{"tool_calls":[{"name":"a","arguments":{}},{"name":"b","arguments":{"z":2}}]}`
		calls, _, ok := parseToolEnvelope(raw)
		if !ok || len(calls) != 2 {
			t.Fatalf("ok=%v calls=%+v", ok, calls)
		}
		if calls[0].Name != "a" || calls[1].Name != "b" {
			t.Fatalf("names %+v", calls)
		}
	})
}

func Test_parse_envelope_arguments_string(t *testing.T) {
	t.Run("parse_envelope_arguments_string", func(t *testing.T) {
		raw := `{"tool_calls":[{"name":"x","arguments":"{\"a\":1}"}]}`
		calls, _, ok := parseToolEnvelope(raw)
		if !ok || len(calls) != 1 {
			t.Fatalf("ok=%v calls=%+v", ok, calls)
		}
		if calls[0].Arguments != `{"a":1}` {
			t.Fatalf("Arguments=%q want literal {\"a\":1}", calls[0].Arguments)
		}
	})
}

func Test_parse_envelope_with_preamble(t *testing.T) {
	t.Run("parse_envelope_with_preamble", func(t *testing.T) {
		raw := "Let me think.\n{\"tool_calls\":[{\"name\":\"x\",\"arguments\":{\"a\":1}}]}"
		calls, prefix, ok := parseToolEnvelope(raw)
		if !ok || len(calls) != 1 {
			t.Fatalf("ok=%v calls=%+v", ok, calls)
		}
		if prefix != "Let me think." {
			t.Fatalf("prefix=%q", prefix)
		}
	})
}

func Test_parse_envelope_invalid_json(t *testing.T) {
	t.Run("parse_envelope_invalid_json", func(t *testing.T) {
		if _, _, ok := parseToolEnvelope(`{"tool_calls":[invalid]}`); ok {
			t.Fatalf("want ok=false")
		}
	})
}

func Test_parse_envelope_no_tool_calls_key(t *testing.T) {
	t.Run("parse_envelope_no_tool_calls_key", func(t *testing.T) {
		if _, _, ok := parseToolEnvelope(`{"other":[]}`); ok {
			t.Fatalf("want ok=false")
		}
	})
}

func Test_render_preamble_choice_none(t *testing.T) {
	t.Run("render_preamble_choice_none", func(t *testing.T) {
		tools := []Tool{{Name: "foo", Description: "d", Parameters: json.RawMessage(`{}`)}}
		if got := renderToolsPreamble(tools, "none"); got != "" {
			t.Fatalf("want empty, got %q", got)
		}
	})
}

func Test_render_preamble_choice_required(t *testing.T) {
	t.Run("render_preamble_choice_required", func(t *testing.T) {
		tools := []Tool{{Name: "foo", Description: "d", Parameters: json.RawMessage(`{}`)}}
		got := renderToolsPreamble(tools, "required")
		if !strings.Contains(got, "MUST call exactly one tool") {
			t.Fatalf("missing required constraint: %q", got)
		}
	})
}

func Test_render_preamble_choice_named(t *testing.T) {
	t.Run("render_preamble_choice_named", func(t *testing.T) {
		tools := []Tool{
			{Name: "foo", Description: "fd", Parameters: json.RawMessage(`{"type":"object"}`)},
			{Name: "bar", Description: "bd", Parameters: json.RawMessage(`{}`)},
		}
		got := renderToolsPreamble(tools, "foo")
		if !strings.Contains(got, "MUST call the tool named foo") {
			t.Fatalf("missing named constraint: %q", got)
		}
		if strings.Contains(got, "name: bar") {
			t.Fatalf("should not list bar: %q", got)
		}
		if !strings.Contains(got, "name: foo") {
			t.Fatalf("want foo listed: %q", got)
		}
	})
}

func Test_render_preamble_auto(t *testing.T) {
	t.Run("render_preamble_auto", func(t *testing.T) {
		tools := []Tool{
			{Name: "foo", Description: "a", Parameters: json.RawMessage(`{}`)},
			{Name: "bar", Description: "b", Parameters: json.RawMessage(`{}`)},
		}
		got := renderToolsPreamble(tools, "auto")
		if !strings.Contains(got, "name: foo") || !strings.Contains(got, "name: bar") {
			t.Fatalf("want both tools: %q", got)
		}
		if strings.Contains(got, "MUST call exactly one tool") ||
			strings.Contains(got, "MUST call the tool named") {
			t.Fatalf("unexpected forced-tool lines: %q", got)
		}
	})
}
