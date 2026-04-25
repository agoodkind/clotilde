package compact

import (
	"strings"
	"testing"
)

func TestBuildProbeArgsForksByDefault(t *testing.T) {
	args := buildProbeArgs(ProbeOptions{
		SessionID:   "sess-123",
		ForkSession: true,
	})
	joined := joinArgs(args)
	for _, want := range []string{
		"-p",
		"--resume sess-123",
		"--fork-session",
		"--session-id ",
		"-n clyde-probe-",
		"--no-session-persistence",
	} {
		if !contains(joined, want) {
			t.Fatalf("args missing %q: %s", want, joined)
		}
	}
}

func TestBuildProbeArgsCanSkipForking(t *testing.T) {
	args := buildProbeArgs(ProbeOptions{
		SessionID:   "sess-123",
		ForkSession: false,
	})
	joined := joinArgs(args)
	if contains(joined, "--fork-session") {
		t.Fatalf("args unexpectedly enabled forking: %s", joined)
	}
}

func joinArgs(args []string) string {
	out := ""
	for i, arg := range args {
		if i > 0 {
			out += " "
		}
		out += arg
	}
	return out
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}
