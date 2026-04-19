package ui

import "fmt"

// ANSI escape sequences for the CLI success/warning/info output helpers.
// We emit raw codes so that output.go has zero dependency on a styling
// library. The codes wrap only the icon, leaving the message text plain
// so that machine-parsed stdout stays predictable.
const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
)

// Success renders a success message with a green checkmark.
func Success(msg string) string {
	return fmt.Sprintf("%s✓%s %s", ansiGreen, ansiReset, msg)
}

// Warning renders a warning message with a yellow warning icon.
func Warning(msg string) string {
	return fmt.Sprintf("%s⚠%s %s", ansiYellow, ansiReset, msg)
}
