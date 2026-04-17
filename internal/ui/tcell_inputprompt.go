package ui

import "github.com/gdamore/tcell/v2"

// InputModel is the public handle for a one-line prompt. The legacy API
// exposed NewInput(prompt) + RunInput(model); both are preserved here.
type InputModel struct {
	Prompt string
}

// NewInput constructs a prompt.
func NewInput(prompt string) InputModel {
	return InputModel{Prompt: prompt}
}

// RunInput shows a centered input prompt and returns the entered text,
// or an empty string if the user cancels.
func RunInput(m InputModel) (string, error) {
	var result string
	err := runOverlay(func(done func()) Widget {
		ti := NewTextInput(m.Prompt + " ")
		ti.OnSubmit = func(s string) {
			result = s
			done()
		}
		ti.OnCancel = func() {
			result = ""
			done()
		}
		return &inputCentered{input: ti, prompt: m.Prompt}
	})
	return result, err
}

type inputCentered struct {
	prompt string
	input  *TextInput
	rect   Rect
}

func (c *inputCentered) Draw(scr tcell.Screen, r Rect) {
	c.rect = r
	w := 60
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 5
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}

	clearRect(scr, box)
	style := StyleDefault.Foreground(ColorBorder)
	scr.SetContent(box.X, box.Y, '┌', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, style)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, style)
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, style)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, style)
	}
	for y := box.Y + 1; y < box.Y+box.H-1; y++ {
		scr.SetContent(box.X, y, '│', nil, style)
		scr.SetContent(box.X+box.W-1, y, '│', nil, style)
	}
	// Hint line
	hint := "enter submit   esc cancel"
	drawString(scr, box.X+2, box.Y+3, StyleMuted, hint, box.W-4)
	c.input.Draw(scr, Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: 1})
}

func (c *inputCentered) HandleEvent(ev tcell.Event) bool {
	return c.input.HandleEvent(ev)
}
