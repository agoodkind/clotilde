package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// ReturnPrompt is the modal that appears when the main TUI launches from a
// freshly exited session. It displays a block of session stats and three
// clearly labeled options. The default highlighted option is Quit so a
// single Enter keypress quits when the user just wants out.
//
// Options:
//
//	0  Return to session
//	1  Go back to session list
//	2  Quit clyde       (default, highlighted on open)
//
// Stats rendered above the options include token count, message count,
// compactions, session age, and basedir. The values come from the App
// at construction time so this widget stays pure rendering.
type ReturnPrompt struct {
	SessionName string
	Stats       []ReturnPromptStat // key/value pairs rendered as a block
	Index       int                // 0 resume, 1 compact, 2 list, 3 quit
	Rect        Rect

	OnResume  func()
	OnCompact func()
	OnList    func()
	OnQuit    func()
	OnCancel  func() // Esc: same as OnList (dismiss to the full table)
}

// ReturnPromptStat is one labeled statistic shown in the modal body.
type ReturnPromptStat struct {
	Label string
	Value string
}

const returnOptionCount = 4

// Draw renders a centered modal with a stats block and a three row menu.
// Dimensions scale to the longest line so the box never clips content.
func (p *ReturnPrompt) Draw(scr tcell.Screen, r Rect) {
	title := "Session exited"
	resumeLine := fmt.Sprintf("Return to %s", p.SessionName)
	compactLine := "Compact this session..."
	listLine := "Go back to session list"
	quitLine := "Quit clyde"

	// Width: the longer of the widest stat row and the widest option.
	widest := runeCount(title)
	widest = imax(widest, runeCount(resumeLine))
	widest = imax(widest, runeCount(compactLine))
	widest = imax(widest, runeCount(listLine))
	widest = imax(widest, runeCount(quitLine))
	for _, s := range p.Stats {
		w := runeCount(s.Label) + 2 + runeCount(s.Value)
		if w > widest {
			widest = w
		}
	}
	w := widest + 10
	if w > r.W-4 {
		w = r.W - 4
	}

	// Height: title, blank, stats lines, blank, 3 options, blank, hint.
	h := 1 + 1 + 1 + len(p.Stats) + 1 + returnOptionCount + 1 + 1 + 1
	if h > r.H-2 {
		h = r.H - 2
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + imax(1, (r.H-h)/2), W: w, H: h}
	p.Rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorBorder)

	inner := Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: box.H - 2}
	y := inner.Y

	drawString(scr, inner.X, y, StyleHeader, title, inner.W)
	y++
	y++

	// Stats block: label left, value right.
	labelStyle := StyleMuted
	valueStyle := StyleDefault.Bold(true)
	for _, s := range p.Stats {
		if y >= inner.Y+inner.H {
			break
		}
		labelCol := inner.X
		valueCol := inner.X + inner.W - runeCount(s.Value)
		drawString(scr, labelCol, y, labelStyle, s.Label, inner.W)
		drawString(scr, valueCol, y, valueStyle, s.Value, inner.W-(valueCol-inner.X))
		y++
	}
	y++

	// Options.
	drawOption(scr, inner.X, y, inner.W, "Return ", resumeLine, p.Index == 0, ColorSuccess)
	y++
	drawOption(scr, inner.X, y, inner.W, "Compact", compactLine, p.Index == 1, ColorWarning)
	y++
	drawOption(scr, inner.X, y, inner.W, "List   ", listLine, p.Index == 2, ColorAccent)
	y++
	drawOption(scr, inner.X, y, inner.W, "Quit   ", quitLine, p.Index == 3, ColorError)
	y++

	// Footer hint sits on the last inner row.
	hint := "↑↓ pick   enter confirm   esc to list"
	drawString(scr, inner.X, inner.Y+inner.H-1, StyleMuted, hint, inner.W)
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

// HandleEvent routes navigation keys to index changes and action keys to the
// registered callbacks. Mouse clicks inside the box are consumed so they do
// not leak through to the table underneath.
//
// The shared HandleMenuKey helper covers Enter / LF / Esc / Up / Down /
// j / k / q. The widget-specific letter shortcuts (r, l, c) are
// handled here before delegating so the cursor index is not advanced
// just because the user pressed an action shortcut.
func (p *ReturnPrompt) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		// Widget-specific letter shortcuts: act directly without
		// touching the cursor.
		if e.Key() == tcell.KeyRune {
			switch e.Rune() {
			case 'r', 'R':
				if p.OnResume != nil {
					p.OnResume()
				}
				return true
			case 'l', 'L':
				if p.OnList != nil {
					p.OnList()
				}
				return true
			case 'c', 'C':
				if p.OnCompact != nil {
					p.OnCompact()
				}
				return true
			}
		}
		// Shared menu navigation. Esc maps to OnCancel, falling
		// back to OnList per the prompt's "esc to list" footer hint.
		cancel := p.OnCancel
		if cancel == nil {
			cancel = p.OnList
		}
		return HandleMenuKey(e, &p.Index, returnOptionCount, MenuKeyOptions{
			OnActivate: func(int) { p.activate() },
			OnCancel:   cancel,
			OnQuit:     p.OnQuit,
			EnableJK:   true,
		})
	case *tcell.EventMouse:
		x, y := e.Position()
		if p.Rect.Contains(x, y) {
			return true
		}
	}
	return false
}

func (p *ReturnPrompt) activate() {
	switch p.Index {
	case 0:
		if p.OnResume != nil {
			p.OnResume()
		}
	case 1:
		if p.OnCompact != nil {
			p.OnCompact()
		}
	case 2:
		if p.OnList != nil {
			p.OnList()
		}
	case 3:
		if p.OnQuit != nil {
			p.OnQuit()
		}
	}
}
