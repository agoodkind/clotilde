package ui

import "github.com/gdamore/tcell/v2"

// MenuKeyOptions configures HandleMenuKey for one modal-style widget.
// Every callback is optional; nil means the corresponding gesture is
// not a shortcut for that widget. The handler always returns true
// when it consumed the event so callers can chain widget-specific
// keys after it without double-handling.
type MenuKeyOptions struct {
	// OnActivate fires on Enter or LF. The handler passes the
	// current cursor index. nil means activation is a no-op (rare).
	OnActivate func(idx int)
	// OnCancel fires on Esc. nil means Esc is ignored.
	OnCancel func()
	// OnQuit fires on q / Q. nil means q is not a shortcut.
	OnQuit func()
	// EnableJK enables nvim-style j / k as Down / Up. Off by default
	// because some widgets reserve j / k as text-input characters.
	EnableJK bool
	// AcceptDigitJump enables 1..9 jumping to the corresponding
	// menu index (one-based, so '1' lands on cursor=0). Off by
	// default  --  only the options popup uses it today.
	AcceptDigitJump bool
}

// HandleMenuKey is the shared keyboard handler for modal widgets
// that show a list of items the user picks via cursor + Enter. It
// consolidates the Enter/LF, Esc, Up/Down, j/k, q, and 1..9 logic
// that was previously copy-pasted across ReturnPrompt, OptionsModal,
// ConfirmModel, and friends.
//
// The single Enter/LF case is the load-bearing bit: tcell delivers
// a CR-keypress as KeyEnter and an LF-keypress as KeyLF, and some
// terminals emit LF after a screen teardown / reinit (which is what
// suspendAndRun does on every resume cycle). Handling both at the
// boundary means widgets never have to think about it again.
//
// Returns true when the event was consumed. cursor is mutated in
// place; the caller passes a pointer to the widget's own Cursor /
// Index field so existing field names stay stable.
func HandleMenuKey(ev *tcell.EventKey, cursor *int, count int, opts MenuKeyOptions) bool {
	if cursor == nil || count <= 0 {
		return false
	}
	switch ev.Key() {
	case tcell.KeyUp:
		if *cursor > 0 {
			*cursor--
		}
		return true
	case tcell.KeyDown:
		if *cursor < count-1 {
			*cursor++
		}
		return true
	case tcell.KeyEnter, tcell.KeyLF:
		if opts.OnActivate != nil {
			opts.OnActivate(*cursor)
		}
		return true
	case tcell.KeyEscape:
		if opts.OnCancel != nil {
			opts.OnCancel()
		}
		return true
	case tcell.KeyRune:
		r := ev.Rune()
		if r == ' ' {
			if opts.OnActivate != nil {
				opts.OnActivate(*cursor)
			}
			return true
		}
		if opts.EnableJK {
			switch r {
			case 'k':
				if *cursor > 0 {
					*cursor--
				}
				return true
			case 'j':
				if *cursor < count-1 {
					*cursor++
				}
				return true
			}
		}
		if opts.AcceptDigitJump && r >= '1' && r <= '9' {
			idx := int(r - '1')
			if idx < count {
				*cursor = idx
				return true
			}
		}
		if (r == 'q' || r == 'Q') && opts.OnQuit != nil {
			opts.OnQuit()
			return true
		}
	}
	return false
}
