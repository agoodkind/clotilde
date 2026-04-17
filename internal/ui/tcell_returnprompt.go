package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// ReturnPrompt is the tiny overlay that appears when the main TUI launches
// from a freshly exited session. It offers two clearly labeled choices on
// separate lines so that "down + enter" always maps to Quit.
//
// The overlay is non-modal in spirit: Esc dismisses it and drops the user
// into the normal session table. Enter commits the currently highlighted
// choice.
type ReturnPrompt struct {
	SessionName string
	Index       int // 0 = resume, 1 = quit
	Rect        Rect

	OnResume func()
	OnQuit   func()
	OnCancel func() // Esc to dismiss without acting
}

// Draw renders a centered two-line prompt with the current option highlighted.
func (p *ReturnPrompt) Draw(scr tcell.Screen, r Rect) {
	// Pick a box that comfortably fits the longer label.
	title := "Session exited"
	resumeLine := fmt.Sprintf("Return to %s", p.SessionName)
	quitLine := "Quit clotilde"

	w := 8 + imax(runeCount(title), imax(runeCount(resumeLine), runeCount(quitLine)))
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 7
	if h > r.H {
		h = r.H
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + 2, W: w, H: h}
	p.Rect = box

	clearRect(scr, box)

	borderStyle := StyleDefault.Foreground(ColorBorder)
	scr.SetContent(box.X, box.Y, '┌', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, borderStyle)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, borderStyle)
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, borderStyle)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, borderStyle)
	}
	for y := box.Y + 1; y < box.Y+box.H-1; y++ {
		scr.SetContent(box.X, y, '│', nil, borderStyle)
		scr.SetContent(box.X+box.W-1, y, '│', nil, borderStyle)
	}

	// Title line
	drawString(scr, box.X+2, box.Y+1, StyleMuted, title, box.W-4)

	// Options
	drawOption(scr, box.X+2, box.Y+3, box.W-4, "Return", resumeLine, p.Index == 0, ColorSuccess)
	drawOption(scr, box.X+2, box.Y+4, box.W-4, "Quit  ", quitLine, p.Index == 1, ColorError)

	// Footer hint
	hint := "↑↓ pick   enter confirm   esc browse list"
	drawString(scr, box.X+2, box.Y+box.H-2, StyleMuted, hint, box.W-4)
}

func drawOption(scr tcell.Screen, x, y, w int, tag, label string, active bool, activeColor tcell.Color) {
	cursor := "  "
	tagStyle := StyleMuted
	labelStyle := StyleSubtext
	if active {
		cursor = "▸ "
		tagStyle = StyleDefault.Foreground(activeColor).Bold(true)
		labelStyle = StyleDefault.Bold(true)
	}
	used := drawString(scr, x, y, tagStyle, cursor, w)
	used += drawString(scr, x+used, y, tagStyle, tag+"  ", w-used)
	drawString(scr, x+used, y, labelStyle, label, w-used)
}

// HandleEvent routes ↑/↓/j/k to index changes and Enter/Esc to the callbacks.
// Mouse clicks on the overlay area are consumed so they do not leak through
// to the table underneath.
func (p *ReturnPrompt) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		switch e.Key() {
		case tcell.KeyUp:
			if p.Index > 0 {
				p.Index--
			}
			return true
		case tcell.KeyDown:
			if p.Index < 1 {
				p.Index++
			}
			return true
		case tcell.KeyEnter:
			if p.Index == 0 {
				if p.OnResume != nil {
					p.OnResume()
				}
			} else if p.OnQuit != nil {
				p.OnQuit()
			}
			return true
		case tcell.KeyEscape:
			if p.OnCancel != nil {
				p.OnCancel()
			}
			return true
		case tcell.KeyRune:
			switch e.Rune() {
			case 'k':
				if p.Index > 0 {
					p.Index--
				}
				return true
			case 'j':
				if p.Index < 1 {
					p.Index++
				}
				return true
			case 'q', 'Q':
				if p.OnQuit != nil {
					p.OnQuit()
				}
				return true
			case 'r', 'R':
				if p.OnResume != nil {
					p.OnResume()
				}
				return true
			}
		}
	case *tcell.EventMouse:
		x, y := e.Position()
		if p.Rect.Contains(x, y) {
			return true
		}
	}
	return false
}
