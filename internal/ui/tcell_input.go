package ui

import "github.com/gdamore/tcell/v2"

// TextInput is a single line text input with a cursor.
type TextInput struct {
	Label    string
	Text     string
	CursorX  int // rune index within Text
	Rect     Rect
	Style    tcell.Style
	OnChange func(s string)
	OnSubmit func(s string)
	OnCancel func()
}

// NewTextInput builds a text input.
func NewTextInput(label string) *TextInput {
	return &TextInput{Label: label, Style: StyleDefault}
}

// Draw renders label + text + cursor into r.
func (ti *TextInput) Draw(scr tcell.Screen, r Rect) {
	ti.Rect = r
	clearRect(scr, r)

	x := r.X
	if ti.Label != "" {
		used := drawString(scr, x, r.Y, StyleMuted, ti.Label, r.W)
		x += used
	}
	style := ti.Style
	if style == (tcell.Style{}) {
		style = StyleDefault
	}
	// Draw text runes
	runes := []rune(ti.Text)
	maxTextW := r.X + r.W - x
	for i, rn := range runes {
		if i >= maxTextW {
			break
		}
		scr.SetContent(x+i, r.Y, rn, nil, style)
	}
	// Draw cursor as a reversed cell at CursorX.
	cursorCol := x + imin(ti.CursorX, len(runes))
	if cursorCol < r.X+r.W {
		var cursorRune rune = ' '
		if ti.CursorX < len(runes) {
			cursorRune = runes[ti.CursorX]
		}
		scr.SetContent(cursorCol, r.Y, cursorRune, nil, style.Reverse(true))
	}
}

// HandleEvent processes key events.
func (ti *TextInput) HandleEvent(ev tcell.Event) bool {
	ek, ok := ev.(*tcell.EventKey)
	if !ok {
		return false
	}
	runes := []rune(ti.Text)
	switch ek.Key() {
	case tcell.KeyEscape:
		if ti.OnCancel != nil {
			ti.OnCancel()
		}
		return true
	case tcell.KeyEnter, tcell.KeyLF:
		if ti.OnSubmit != nil {
			ti.OnSubmit(ti.Text)
		}
		return true
	case tcell.KeyLeft:
		if ti.CursorX > 0 {
			ti.CursorX--
		}
		return true
	case tcell.KeyRight:
		if ti.CursorX < len(runes) {
			ti.CursorX++
		}
		return true
	case tcell.KeyHome:
		ti.CursorX = 0
		return true
	case tcell.KeyEnd:
		ti.CursorX = len(runes)
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if ti.CursorX > 0 {
			runes = append(runes[:ti.CursorX-1], runes[ti.CursorX:]...)
			ti.Text = string(runes)
			ti.CursorX--
			if ti.OnChange != nil {
				ti.OnChange(ti.Text)
			}
		}
		return true
	case tcell.KeyDelete:
		if ti.CursorX < len(runes) {
			runes = append(runes[:ti.CursorX], runes[ti.CursorX+1:]...)
			ti.Text = string(runes)
			if ti.OnChange != nil {
				ti.OnChange(ti.Text)
			}
		}
		return true
	case tcell.KeyCtrlU:
		ti.Text = ""
		ti.CursorX = 0
		if ti.OnChange != nil {
			ti.OnChange(ti.Text)
		}
		return true
	case tcell.KeyRune:
		r := ek.Rune()
		runes = append(runes[:ti.CursorX], append([]rune{r}, runes[ti.CursorX:]...)...)
		ti.Text = string(runes)
		ti.CursorX++
		if ti.OnChange != nil {
			ti.OnChange(ti.Text)
		}
		return true
	}
	return false
}
