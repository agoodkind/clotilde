package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
)

// Modal is a centered overlay with a title, body lines, optional detail
// bullets, and a row of buttons at the bottom. Single line input is not
// built in; use a Form (below) for more complex overlays.
type Modal struct {
	Title       string
	Body        string   // may contain \n for multiple lines
	Details     []string // bullet lines under body
	Buttons     []string
	ActiveIndex int
	Destructive bool // highlight last button in red

	// Keyboard shortcut map. Example: {'y': 1, 'n': 0} clicks button y/n.
	Shortcuts map[rune]int

	// OnChoice fires with the button index on Enter; -1 on Esc.
	OnChoice func(index int)

	Rect Rect
}

// Draw renders a centered modal box inside r.
func (m *Modal) Draw(scr tcell.Screen, r Rect) {
	m.Rect = r

	lines := m.layoutLines(r.W - 6)
	h := min(
		// border top + lines + blank + button row + border bottom
		2+len(lines)+2+1, r.H)
	w := 60
	if nw := longestLine(lines) + 6; nw > w {
		w = nw
	}
	if bw := buttonBarWidth(m.Buttons) + 6; bw > w {
		w = bw
	}
	if w > r.W-4 {
		w = r.W - 4
	}

	mx := r.X + (r.W-w)/2
	my := r.Y + (r.H-h)/2
	box := Rect{X: mx, Y: my, W: w, H: h}
	m.drawBox(scr, box)

	// Draw content inside box
	inner := Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: box.H - 2}
	for i, seg := range lines {
		if i >= inner.H-2 {
			break // leave 2 rows for buttons + blank
		}
		style := StyleDefault
		if seg.isTitle {
			style = StyleDefault.Bold(true)
		} else if seg.isDim {
			style = StyleMuted
		}
		drawString(scr, inner.X, inner.Y+i, style, seg.text, inner.W)
	}

	// Draw button row at bottom of box
	by := box.Y + box.H - 2
	bx := box.X + 2
	bwTotal := buttonBarWidth(m.Buttons)
	if bwTotal < inner.W {
		bx = box.X + (box.W-bwTotal)/2
	}
	for i, label := range m.Buttons {
		style := StyleDefault.Foreground(ColorSubtext)
		if i == m.ActiveIndex {
			if m.Destructive && i == len(m.Buttons)-1 {
				style = tcell.StyleDefault.Background(ColorError).Foreground(tcell.ColorBlack).Bold(true)
			} else {
				style = tcell.StyleDefault.Background(ColorAccent).Foreground(tcell.ColorBlack).Bold(true)
			}
		} else if m.Destructive && i == len(m.Buttons)-1 {
			style = StyleDefault.Foreground(ColorError)
		}
		text := " " + label + " "
		drawString(scr, bx, by, style, text, box.X+box.W-bx)
		bx += runeCount(text) + 2
	}
}

type modalLine struct {
	text    string
	isTitle bool
	isDim   bool
}

func (m *Modal) layoutLines(wrapW int) []modalLine {
	var out []modalLine
	if m.Title != "" {
		out = append(out, modalLine{text: m.Title, isTitle: true})
		out = append(out, modalLine{text: ""})
	}
	if m.Body != "" {
		for bl := range strings.SplitSeq(m.Body, "\n") {
			for _, wrapped := range wrapLine(bl, wrapW) {
				out = append(out, modalLine{text: wrapped})
			}
		}
	}
	if len(m.Details) > 0 {
		out = append(out, modalLine{text: ""})
		for _, d := range m.Details {
			for _, wrapped := range wrapLine("  "+d, wrapW) {
				out = append(out, modalLine{text: wrapped, isDim: true})
			}
		}
	}
	out = append(out, modalLine{text: ""})
	return out
}

// drawBox paints a single-line box (corners + edges) at box.
func (m *Modal) drawBox(scr tcell.Screen, box Rect) {
	// First, fill the box background with default style
	clearRect(scr, box)

	borderStyle := StyleDefault.Foreground(ColorBorder)
	// Corners
	scr.SetContent(box.X, box.Y, '┌', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, borderStyle)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, borderStyle)
	// Horizontal edges
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, borderStyle)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, borderStyle)
	}
	// Vertical edges
	for y := box.Y + 1; y < box.Y+box.H-1; y++ {
		scr.SetContent(box.X, y, '│', nil, borderStyle)
		scr.SetContent(box.X+box.W-1, y, '│', nil, borderStyle)
	}
}

// HandleEvent processes keyboard events; mouse clicks on the modal area are
// consumed so they do not leak to widgets behind.
// HandleEvent routes keyboard and mouse events for the Modal.
//
// Modal navigates buttons left-to-right (Tab/Right or Shift-Tab/Left)
// rather than vertically, so the cursor movement does not match the
// generic up/down model HandleMenuKey assumes. Up/Down are not used
// here. The activate / cancel half delegates to HandleMenuKey so
// the Enter-vs-LF terminal-mode quirk stays fixed in one place.
//
// Custom Shortcuts (e.g. y/n on confirm dialogs) take priority over
// the shared handler so a single key can both cancel AND confirm
// depending on the registered shortcut.
func (m *Modal) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		// Custom rune shortcuts first (y/n etc.).
		if e.Key() == tcell.KeyRune {
			if idx, ok := m.Shortcuts[e.Rune()]; ok {
				if m.OnChoice != nil {
					m.OnChoice(idx)
				}
				return true
			}
		}
		// Horizontal button navigation (Modal-specific).
		switch e.Key() {
		case tcell.KeyTab, tcell.KeyRight:
			if len(m.Buttons) > 0 {
				m.ActiveIndex = (m.ActiveIndex + 1) % len(m.Buttons)
			}
			return true
		case tcell.KeyBacktab, tcell.KeyLeft:
			if len(m.Buttons) > 0 {
				m.ActiveIndex = (m.ActiveIndex - 1 + len(m.Buttons)) % len(m.Buttons)
			}
			return true
		}
		// Shared activate / cancel via HandleMenuKey. Pass a dummy
		// cursor because Modal does not use the helper's vertical
		// navigation. Enter activates the current button; Esc
		// returns -1 (cancel) per the existing OnChoice contract.
		dummy := m.ActiveIndex
		return HandleMenuKey(e, &dummy, len(m.Buttons), MenuKeyOptions{
			OnActivate: func(int) {
				if m.OnChoice != nil {
					m.OnChoice(m.ActiveIndex)
				}
			},
			OnCancel: func() {
				if m.OnChoice != nil {
					m.OnChoice(-1)
				}
			},
		})
	case *tcell.EventMouse:
		// Consume mouse clicks within the modal rect so they don't leak.
		x, y := e.Position()
		if m.Rect.Contains(x, y) {
			return true
		}
	}
	return false
}

func buttonBarWidth(buttons []string) int {
	total := 0
	for i, b := range buttons {
		if i > 0 {
			total += 2
		}
		total += runeCount(b) + 2
	}
	return total
}

func longestLine(lines []modalLine) int {
	n := 0
	for _, l := range lines {
		if w := runeCount(l.text); w > n {
			n = w
		}
	}
	return n
}

// wrapLine wraps s on spaces to fit width w. Returns at least one line.
func wrapLine(s string, w int) []string {
	if w <= 0 || runeCount(s) <= w {
		return []string{s}
	}
	var out []string
	words := strings.Fields(s)
	var cur string
	for _, word := range words {
		cand := cur
		if cand != "" {
			cand += " "
		}
		cand += word
		if runeCount(cand) > w && cur != "" {
			out = append(out, cur)
			cur = word
		} else {
			cur = cand
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}
