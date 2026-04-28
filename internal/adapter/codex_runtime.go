package adapter

import (
	"os"
	"strings"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
)

// Codex runtime hook variables. The codex package exposes function-
// pointer fields so the daemon can override the wall-clock, working
// directory, and shell name without bringing the daemon's
// dependencies into the codex leaf package.

var (
	codexNow       = time.Now
	codexGetwd     = os.Getwd
	codexShellName = func() string {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			return "sh"
		}
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}
)

func init() {
	adaptercodex.NowFunc = func() time.Time { return codexNow() }
	adaptercodex.GetwdFn = func() (string, error) { return codexGetwd() }
	adaptercodex.ShellNameFn = func() string { return codexShellName() }
}
