package fallback

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSanitizePathReplacesNonAlnum(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/Users/agoodkind/code", "-Users-agoodkind-code"},
		{"plugin:mcp:server", "plugin-mcp-server"},
		{"abc123", "abc123"},
		{"/a/b/c", "-a-b-c"},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizePath(c.in); got != c.want {
			t.Errorf("SanitizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizePathTruncatesLongPaths(t *testing.T) {
	in := strings.Repeat("a", 250)
	out := SanitizePath(in)
	if len(out) == len(in) {
		t.Fatalf("expected truncation, got same length %d", len(out))
	}
	if !strings.HasPrefix(out, strings.Repeat("a", maxSanitizedLength)) {
		t.Fatalf("truncated prefix mismatch: %s", out)
	}
}

func TestTranscriptPathComposesCorrectly(t *testing.T) {
	p := TranscriptPath("/home/u/.claude", "/home/u/work/proj", "abc-123")
	want := "/home/u/.claude/projects/-home-u-work-proj/abc-123.jsonl"
	if p != want {
		t.Fatalf("TranscriptPath mismatch:\n got  %s\n want %s", p, want)
	}
}

func TestSynthesizeTranscriptBuildsParentChain(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "more"},
	}
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	lines, err := SynthesizeTranscript(msgs, "sess-1", "/scratch", now)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d", len(lines))
	}
	if lines[0].ParentUUID != nil {
		t.Fatalf("first line parentUUID must be nil, got %v", *lines[0].ParentUUID)
	}
	if lines[1].ParentUUID == nil || *lines[1].ParentUUID != lines[0].UUID {
		t.Fatalf("second line parentUUID != first UUID: %+v", lines[1].ParentUUID)
	}
	if lines[2].ParentUUID == nil || *lines[2].ParentUUID != lines[1].UUID {
		t.Fatalf("third line parentUUID != second UUID: %+v", lines[2].ParentUUID)
	}
	if lines[1].Type != "assistant" {
		t.Fatalf("expected assistant type, got %q", lines[1].Type)
	}
}

func TestSynthesizeTranscriptDeterministic(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "same"},
		{Role: "assistant", Content: "reply"},
	}
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	a, _ := SynthesizeTranscript(msgs, "sess-X", "/cwd", now)
	b, _ := SynthesizeTranscript(msgs, "sess-X", "/cwd", now)
	for i := range a {
		if a[i].UUID != b[i].UUID {
			t.Fatalf("uuid at %d drifted: %s vs %s", i, a[i].UUID, b[i].UUID)
		}
	}
}

func TestSynthesizeTranscriptSkipsSystem(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
	}
	lines, _ := SynthesizeTranscript(msgs, "sess", "/cwd", time.Now())
	if len(lines) != 1 {
		t.Fatalf("system lines must be skipped, got %d", len(lines))
	}
}

func TestWriteTranscriptRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "projects", "-cwd", "sess.jsonl")
	lines := []TranscriptLine{
		{
			Type:      "user",
			UUID:      "u1",
			SessionID: "sess",
			Message:   json.RawMessage(`{"role":"user","content":"hi"}`),
			Cwd:       "/cwd",
			UserType:  "user",
			Version:   TranscriptVersion,
			Timestamp: "2026-04-21T00:00:00Z",
		},
	}
	if err := WriteTranscript(path, lines); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), `"uuid":"u1"`) {
		t.Fatalf("expected uuid in output, got:\n%s", data)
	}
	if !strings.HasSuffix(string(data), "\n") {
		t.Fatalf("JSONL must end with newline")
	}
}

func TestBuildArgsResumeDropsSessionID(t *testing.T) {
	r := Request{
		Model:     "sonnet",
		SessionID: "abc",
		Resume:    true,
		Messages: []Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "reply"},
			{Role: "user", Content: "latest"},
		},
	}
	args := buildArgs(r)
	found := false
	for i, a := range args {
		if a == "--session-id" {
			t.Fatalf("--session-id should be absent in resume mode: %v", args)
		}
		if a == "--resume" && i+1 < len(args) && args[i+1] == "abc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected --resume abc in resume mode, got: %v", args)
	}
	// Positional prompt must be just the latest user message.
	if args[len(args)-1] != "latest" {
		t.Fatalf("positional prompt should be latest user message only, got %q", args[len(args)-1])
	}
}

func TestBuildArgsLegacyKeepsSessionID(t *testing.T) {
	r := Request{
		Model:     "sonnet",
		SessionID: "abc",
		Messages: []Message{
			{Role: "user", Content: "hi"},
		},
	}
	args := buildArgs(r)
	has := false
	for i, a := range args {
		if a == "--session-id" && i+1 < len(args) && args[i+1] == "abc" {
			has = true
		}
		if a == "--resume" {
			t.Fatalf("--resume should not fire without Resume=true: %v", args)
		}
	}
	if !has {
		t.Fatalf("expected --session-id abc in legacy mode, got: %v", args)
	}
}
