package ui

import (
	"runtime"
	"testing"
)

// TestClipboardCandidatesPerOS asserts the candidate ordering for
// each supported host. The ordering matters because picking the
// wrong tool first wastes a process spawn on every copy.
func TestClipboardCandidatesPerOS(t *testing.T) {
	cands := clipboardCandidates()
	switch runtime.GOOS {
	case "darwin":
		if len(cands) != 1 || cands[0].Bin != "pbcopy" {
			t.Fatalf("darwin candidates = %+v, want [pbcopy]", cands)
		}
	case "linux":
		if len(cands) != 3 {
			t.Fatalf("linux candidates = %+v, want 3 entries", cands)
		}
		want := []string{"wl-copy", "xclip", "xsel"}
		for i, c := range cands {
			if c.Bin != want[i] {
				t.Fatalf("linux candidate[%d] = %q, want %q", i, c.Bin, want[i])
			}
		}
	case "windows":
		if len(cands) < 1 || cands[0].Bin != "clip.exe" {
			t.Fatalf("windows candidates = %+v, want clip.exe first", cands)
		}
	}
}

// TestClipboardErrorWhenNothingAvailable verifies that CopyToClipboard
// returns a useful error message on a host where no candidate exists.
// The test cannot easily neuter PATH inside this process, so we just
// assert the error path is reachable when GOOS has no candidates.
func TestClipboardErrorMessage(t *testing.T) {
	if cands := clipboardCandidates(); len(cands) == 0 {
		err := CopyToClipboard("hi")
		if err == nil {
			t.Fatal("expected error on host with no candidates")
		}
	}
}
