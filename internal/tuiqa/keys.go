package tuiqa

import "strings"

// KeyBytes maps a single symbolic key name to bytes for PTY and iTerm injection.
// Names align with common tmux send-keys tokens where possible.
func KeyBytes(name string) []byte {
	switch name {
	case "Enter":
		return []byte{0x0D}
	case "Tab":
		return []byte{0x09}
	case "BSpace", "Backspace":
		return []byte{0x7F}
	case "Escape", "Esc":
		return []byte{0x1B}
	case "Space":
		return []byte{0x20}
	case "Up":
		return []byte{0x1B, 0x5B, 0x41}
	case "Down":
		return []byte{0x1B, 0x5B, 0x42}
	case "Right":
		return []byte{0x1B, 0x5B, 0x43}
	case "Left":
		return []byte{0x1B, 0x5B, 0x44}
	case "PageUp":
		return []byte{0x1B, 0x5B, 0x35, 0x7E}
	case "PageDown":
		return []byte{0x1B, 0x5B, 0x36, 0x7E}
	default:
		if len(name) == 1 {
			return []byte(name)
		}
		return []byte(name)
	}
}

// TokenToBytes maps one tmux-style token (including C-x control) to bytes.
func TokenToBytes(tok string) []byte {
	if strings.HasPrefix(tok, "C-") && len(tok) == 3 {
		ch := tok[2]
		if ch >= 'a' && ch <= 'z' {
			return []byte{ch - 'a' + 1}
		}
		if ch >= 'A' && ch <= 'Z' {
			return []byte{ch - 'A' + 1}
		}
	}
	if strings.HasPrefix(tok, "^") && len(tok) == 2 {
		return []byte{tok[1] & 0x1f}
	}
	return KeyBytes(tok)
}
