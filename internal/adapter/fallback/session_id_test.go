package fallback

import "testing"

func TestDeriveSessionIDIsStableForSameInputs(t *testing.T) {
	a := DeriveSessionID("hello world", "sonnet")
	b := DeriveSessionID("hello world", "sonnet")
	if a == "" {
		t.Fatal("expected non-empty UUID")
	}
	if a != b {
		t.Fatalf("expected stable UUID, got %q vs %q", a, b)
	}
}

func TestDeriveSessionIDChangesWhenFirstMessageChanges(t *testing.T) {
	a := DeriveSessionID("hello world", "sonnet")
	b := DeriveSessionID("hello world!", "sonnet")
	if a == b {
		t.Fatalf("expected different UUIDs for different first-message text, got %q twice", a)
	}
}

func TestDeriveSessionIDChangesWhenModelChanges(t *testing.T) {
	a := DeriveSessionID("hello world", "sonnet")
	b := DeriveSessionID("hello world", "opus")
	if a == b {
		t.Fatalf("expected different UUIDs for different model alias, got %q twice", a)
	}
}

func TestDeriveSessionIDEmptyInputYieldsEmpty(t *testing.T) {
	if got := DeriveSessionID("", "sonnet"); got != "" {
		t.Fatalf("expected empty UUID for empty first message, got %q", got)
	}
	if got := DeriveSessionID("   \t\n ", "sonnet"); got != "" {
		t.Fatalf("expected empty UUID for whitespace-only first message, got %q", got)
	}
}

func TestDeriveSessionIDShape(t *testing.T) {
	got := DeriveSessionID("hello", "sonnet")
	// Canonical UUID format: 8-4-4-4-12 hex digits separated by '-'.
	// UUIDv4 variant bits mean position 14 is '4' and position 19 is one
	// of 8/9/a/b.
	if len(got) != 36 {
		t.Fatalf("length = %d want 36 (%q)", len(got), got)
	}
	if got[8] != '-' || got[13] != '-' || got[18] != '-' || got[23] != '-' {
		t.Fatalf("dashes misplaced in %q", got)
	}
	if got[14] != '4' {
		t.Fatalf("version nibble = %q want 4 (%q)", got[14], got)
	}
	v := got[19]
	if v != '8' && v != '9' && v != 'a' && v != 'b' {
		t.Fatalf("variant nibble = %q want 8/9/a/b (%q)", v, got)
	}
}

func TestFirstUserMessageSkipsSystemAndAssistant(t *testing.T) {
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "hi"},
		{Role: "user", Content: "first user text"},
		{Role: "user", Content: "second user text"},
	}
	if got := firstUserMessage(msgs); got != "first user text" {
		t.Fatalf("got %q", got)
	}
}

func TestFirstUserMessageEmpty(t *testing.T) {
	if got := firstUserMessage(nil); got != "" {
		t.Fatalf("nil msgs: got %q", got)
	}
	if got := firstUserMessage([]Message{{Role: "system", Content: "x"}}); got != "" {
		t.Fatalf("no user: got %q", got)
	}
}

func TestBuildArgsIncludesSessionIDWhenSet(t *testing.T) {
	uid := DeriveSessionID("hi there", "sonnet")
	args := buildArgs(Request{
		Model:     "sonnet",
		SessionID: uid,
		Messages:  []Message{{Role: "user", Content: "hi there"}},
	})
	if !argsContainPair(args, "--session-id", uid) {
		t.Fatalf("expected --session-id %s in argv: %v", uid, args)
	}
}

func TestBuildArgsOmitsSessionIDWhenEmpty(t *testing.T) {
	args := buildArgs(Request{
		Model:    "sonnet",
		Messages: []Message{{Role: "user", Content: "hi"}},
	})
	for _, a := range args {
		if a == "--session-id" {
			t.Fatalf("expected no --session-id flag when SessionID is empty: %v", args)
		}
	}
}

func TestBuildArgsAlwaysSetsOutputFormatStreamJSON(t *testing.T) {
	args := buildArgs(Request{Model: "sonnet"})
	if !argsContainPair(args, "--output-format", "stream-json") {
		t.Fatalf("expected --output-format stream-json: %v", args)
	}
}

func argsContainPair(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}
