package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// ViewerModel is the public handle for a scrollable text viewer.
type ViewerModel struct {
	Title   string
	Content string
}

// NewViewer constructs a viewer over the given plain-text content.
func NewViewer(title, content string) ViewerModel {
	return ViewerModel{Title: title, Content: content}
}

// RunViewer opens a full-screen viewer. Returns when the user presses q or Esc.
func RunViewer(m ViewerModel) error {
	return runOverlay(func(done func()) Widget {
		tb := &TextBox{
			Title:      " " + m.Title + " ",
			TitleStyle: StyleHeaderBar.Bold(true),
			Wrap:       true,
			Focused:    true,
		}
		tb.SetLines(strings.Split(m.Content, "\n"))
		return &fullScreenViewer{box: tb, onClose: done}
	})
}

type fullScreenViewer struct {
	box     *TextBox
	onClose func()
	rect    Rect
}

func (v *fullScreenViewer) Draw(scr tcell.Screen, r Rect) {
	v.rect = r
	// Title row
	fillRow(scr, r.X, r.Y, r.W, StyleHeaderBar)
	drawString(scr, r.X+1, r.Y, StyleHeaderBar.Bold(true), v.box.Title, r.W-1)
	// Footer
	fillRow(scr, r.X, r.Y+r.H-1, r.W, StyleStatusBar)
	drawString(scr, r.X+1, r.Y+r.H-1, StyleStatusBar,
		" ↑↓ PgUp/PgDn scroll   g/G top/bottom   q/esc close ", r.W-1)
	// Body
	body := Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}
	vbox := &TextBox{
		Lines:   v.box.Lines,
		Wrap:    v.box.Wrap,
		Offset:  v.box.Offset,
		Focused: true,
	}
	vbox.Draw(scr, body)
	v.box.Offset = vbox.Offset
}

func (v *fullScreenViewer) HandleEvent(ev tcell.Event) bool {
	if ek, ok := ev.(*tcell.EventKey); ok {
		if ek.Key() == tcell.KeyEscape || (ek.Key() == tcell.KeyRune && ek.Rune() == 'q') {
			if v.onClose != nil {
				v.onClose()
			}
			return true
		}
	}
	if em, ok := ev.(*tcell.EventMouse); ok {
		btns := em.Buttons()
		if btns&tcell.WheelUp != 0 {
			v.box.Offset = imax(0, v.box.Offset-3)
			return true
		}
		if btns&tcell.WheelDown != 0 {
			v.box.Offset += 3
			return true
		}
	}
	return v.box.HandleEvent(ev)
}
