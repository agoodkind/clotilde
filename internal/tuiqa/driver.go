// Package tuiqa drives the clyde TUI through tmux, a PTY plus vt10x, or iTerm2
// for agent iteration and manual QA.
package tuiqa

import (
	"fmt"
	"strings"
)

// Driver controls a running clyde TUI in a terminal backend.
type Driver interface {
	Name() string
	// Start launches the binary with the given environment (KEY=value).
	Start(binaryPath string, env []string, cols, rows int) error
	Kill() error
	Capture() (string, error)
	// SendKey passes tokens in tmux send-keys form (e.g. Enter, Tab, C-c, a).
	SendKey(tokens []string) error
	PasteRaw(b []byte) error
	SessionAlive() bool
}

// New constructs a driver by name: tmux, pty, iterm.
func New(name string) (Driver, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "tmux":
		return newTmuxDriver(), nil
	case "pty":
		return newPTYDriver(), nil
	case "iterm":
		return newITermDriver()
	default:
		return nil, fmt.Errorf("tuiqa: unknown driver %q (want tmux, pty, iterm)", name)
	}
}

// ConfigureSessionName sets a stable tmux session name when name is non-empty.
func ConfigureSessionName(d Driver, name string) {
	if name == "" {
		return
	}
	if tm, ok := d.(*tmuxDriver); ok {
		tm.SetSessionName(name)
	}
}

// TmuxSessionName returns the tmux session name if d is a tmux driver.
func TmuxSessionName(d Driver) string {
	tm, ok := d.(*tmuxDriver)
	if !ok {
		return ""
	}
	return tm.session
}
