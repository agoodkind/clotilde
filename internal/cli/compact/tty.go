package compact

import (
	"io"
	"os"

	"golang.org/x/term"
)

// isTerminal reports whether w is writing to an interactive terminal.
// Used to choose between in-place progress redraws (TTY) and one-line-
// per-iteration output (pipes, hook captures, test harnesses).
func isTerminal(w io.Writer) bool {
	type fd interface {
		Fd() uintptr
	}
	if f, ok := w.(fd); ok {
		return term.IsTerminal(int(f.Fd()))
	}
	if w == os.Stdout {
		return term.IsTerminal(int(os.Stdout.Fd()))
	}
	if w == os.Stderr {
		return term.IsTerminal(int(os.Stderr.Fd()))
	}
	return false
}
