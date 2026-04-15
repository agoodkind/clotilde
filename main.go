package main

import (
	"os"

	"github.com/fgrehm/clotilde/cmd"
)

func main() {
	// Classify and optionally rewrite args before cobra sees them.
	// This is what makes clotilde a drop-in replacement for claude.
	if len(os.Args) > 1 {
		mode, rewritten := cmd.ClassifyArgs(os.Args[1:])
		switch mode {
		case cmd.ModePassthrough:
			os.Exit(cmd.ForwardToClaude(os.Args[1:]))
		case cmd.ModeResumeFlag:
			os.Args = append(os.Args[:1], rewritten...)
			// ModeClotilde: fall through — cobra handles it
		}
	}

	cmd.Execute()
}
