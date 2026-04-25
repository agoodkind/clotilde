package ui

import (
	"strings"
	"testing"
	"time"

	"goodkind.io/clyde/internal/session"
)

func TestDetailsView_ContextUsesLoadingAndDiagnosticsInsteadOfUnavailable(t *testing.T) {
	view := NewDetailsView()
	sess := &session.Session{
		Name: "demo",
		Metadata: session.Metadata{
			Name:          "demo",
			SessionID:     "1234",
			WorkspaceRoot: "/Users/test/Sites/demo",
			Created:       time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC),
			LastAccessed:  time.Date(2026, 4, 24, 9, 5, 0, 0, time.UTC),
		},
	}
	detail := SessionDetail{
		Model:              "opus",
		LastMessageTokens:  44,
		ContextUsageStatus: "failed; retrying",
	}

	lines := flattenSegments(view.buildLeft(sess, detail))
	joined := strings.Join(lines, "\n")

	if strings.Contains(joined, "unavailable") {
		t.Fatalf("details pane should not render unavailable:\n%s", joined)
	}
	if !strings.Contains(joined, "Context") || !strings.Contains(joined, "loading...") {
		t.Fatalf("details pane missing loading context row:\n%s", joined)
	}
	if !strings.Contains(joined, "Context probe") || !strings.Contains(joined, "failed; retrying") {
		t.Fatalf("details pane missing retry diagnostic:\n%s", joined)
	}
	if !strings.Contains(joined, "Last msg") || !strings.Contains(joined, "~44 tok") {
		t.Fatalf("details pane missing renamed last message row:\n%s", joined)
	}
}

func TestDetailsView_ContextShowsExactUsageWhenLoaded(t *testing.T) {
	view := NewDetailsView()
	sess := &session.Session{
		Name: "demo",
		Metadata: session.Metadata{
			Name:          "demo",
			SessionID:     "1234",
			WorkspaceRoot: "/Users/test/Sites/demo",
			Created:       time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC),
			LastAccessed:  time.Date(2026, 4, 24, 9, 5, 0, 0, time.UTC),
		},
	}
	detail := SessionDetail{
		Model: "opus",
		ContextUsage: SessionContextUsage{
			TotalTokens:    84320,
			MaxTokens:      200000,
			Percentage:     42,
			MessagesTokens: 41234,
		},
		ContextUsageLoaded: true,
	}

	lines := flattenSegments(view.buildLeft(sess, detail))
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "84.3k/200k tok  42%") {
		t.Fatalf("details pane missing exact context usage:\n%s", joined)
	}
	if !strings.Contains(joined, "Msg tokens") || !strings.Contains(joined, "~41.2k tok") {
		t.Fatalf("details pane missing message token row:\n%s", joined)
	}
	if strings.Contains(joined, "Diagnostics") {
		t.Fatalf("details pane should not show diagnostics for successful probe:\n%s", joined)
	}
}

func TestFormatCompactTokens(t *testing.T) {
	cases := map[int]string{
		44:      "44",
		1000:    "1k",
		41234:   "41.2k",
		100000:  "100k",
		200000:  "200k",
		1000000: "1M",
		1250000: "1.2M",
	}
	for in, want := range cases {
		if got := formatCompactTokens(in); got != want {
			t.Fatalf("formatCompactTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func flattenSegments(lines [][]TextSegment) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		var b strings.Builder
		for _, seg := range line {
			b.WriteString(seg.Text)
		}
		out = append(out, b.String())
	}
	return out
}
