package ui

import (
	"github.com/gdamore/tcell/v2"
)

// OptionsModalEntry is one selectable row in the options popup.
// Hint is the short description rendered in dim text on the right.
// Disabled entries draw muted and ignore activation.
type OptionsModalEntry struct {
	Label    string
	Hint     string
	Action   func()
	Disabled bool
}

// OptionsModal is the popup that appears when the user presses Enter on
// a session row. It replaces the implicit "Enter resumes" behavior with
// an explicit menu so destructive actions like delete are not one
// keystroke away from the most common navigation key.
type OptionsModal struct {
	Title   string
	Entries []OptionsModalEntry

	cursor int
	rect   Rect

	// OnCancel fires when the user dismisses with Esc, q, or a click
	// outside the modal. The dialog removes itself on activation already.
	OnCancel func()
}

// NewOptionsModal builds a modal with the cursor on the first enabled
// entry. Entries with no action set are treated as disabled.
func NewOptionsModal(title string, entries []OptionsModalEntry) *OptionsModal {
	m := &OptionsModal{Title: title, Entries: entries}
	for i, e := range entries {
		if !e.Disabled && e.Action != nil {
			m.cursor = i
			break
		}
	}
	return m
}

func (m *OptionsModal) Draw(scr tcell.Screen, r Rect) {
	w := r.W - 8
	if w < 40 {
		w = 40
	}
	if w > r.W-4 {
		w = r.W - 4
	}
	contentRows := len(m.Entries) + 2 // title + blank + entries; we already account for title in h math
	h := contentRows + 4
	if h > r.H-2 {
		h = r.H - 2
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	m.rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorAccent)

	titleStyle := StyleDefault.Foreground(ColorAccent).Bold(true)
	if m.Title != "" {
		drawString(scr, box.X+2, box.Y, titleStyle, " "+m.Title+" ", box.W-4)
	}

	y := box.Y + 2
	for i, e := range m.Entries {
		style := StyleDefault.Foreground(ColorText)
		marker := "  "
		if e.Disabled || e.Action == nil {
			style = StyleMuted
		} else if i == m.cursor {
			style = StyleDefault.Foreground(ColorAccent).Bold(true)
			marker = "▸ "
		}
		drawString(scr, box.X+2, y, style, marker+e.Label, box.W-4)
		if e.Hint != "" {
			hintX := box.X + box.W - 2 - runeCount(e.Hint)
			if hintX > box.X+2+runeCount(marker+e.Label)+2 {
				drawString(scr, hintX, y, StyleMuted, e.Hint, box.W-4)
			}
		}
		y++
	}

	hint := "  ↑↓ navigate · enter activate · esc cancel"
	drawString(scr, box.X+2, box.Y+box.H-1, StyleMuted, hint, box.W-4)
}

func (m *OptionsModal) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		return m.handleKey(e)
	case *tcell.EventMouse:
		x, y := e.Position()
		if !m.rect.Contains(x, y) {
			if e.Buttons() != 0 && m.OnCancel != nil {
				m.OnCancel()
			}
			return true
		}
		if e.Buttons()&tcell.ButtonPrimary != 0 {
			row := y - (m.rect.Y + 2)
			if row >= 0 && row < len(m.Entries) {
				m.cursor = row
				m.activate()
			}
		}
		return true
	}
	return false
}

// handleKey routes navigation keys. Cursor movement stays local so
// the skip-disabled-entry loop in moveCursor is preserved; the
// remaining gestures (Enter / LF / Esc / q) delegate to the shared
// HandleMenuKey helper so the Enter-vs-LF terminal-mode bug cannot
// resurface here.
func (m *OptionsModal) handleKey(e *tcell.EventKey) bool {
	// Local navigation: respects the skip-disabled-entry walk.
	switch e.Key() {
	case tcell.KeyUp:
		m.moveCursor(-1)
		return true
	case tcell.KeyDown:
		m.moveCursor(+1)
		return true
	case tcell.KeyRune:
		switch e.Rune() {
		case 'j':
			m.moveCursor(+1)
			return true
		case 'k':
			m.moveCursor(-1)
			return true
		}
	}
	// Shared handler for Enter / LF / Esc / q. Pass a throwaway
	// cursor pointer because the helper would otherwise increment
	// our cursor on Up / Down (which we already handled above).
	dummy := m.cursor
	return HandleMenuKey(e, &dummy, len(m.Entries), MenuKeyOptions{
		OnActivate: func(int) { m.activate() },
		OnCancel:   m.OnCancel,
		OnQuit:     m.OnCancel, // OptionsModal treats q as cancel.
	})
}

func (m *OptionsModal) moveCursor(delta int) {
	if len(m.Entries) == 0 {
		return
	}
	start := m.cursor
	for {
		m.cursor += delta
		if m.cursor < 0 {
			m.cursor = len(m.Entries) - 1
		}
		if m.cursor >= len(m.Entries) {
			m.cursor = 0
		}
		e := m.Entries[m.cursor]
		if !e.Disabled && e.Action != nil {
			return
		}
		if m.cursor == start {
			return
		}
	}
}

func (m *OptionsModal) activate() {
	if m.cursor < 0 || m.cursor >= len(m.Entries) {
		return
	}
	e := m.Entries[m.cursor]
	if e.Disabled || e.Action == nil {
		return
	}
	e.Action()
}
