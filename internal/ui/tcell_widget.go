package ui

import "github.com/gdamore/tcell/v2"

// Rect is a screen rectangle.
type Rect struct{ X, Y, W, H int }

// Contains reports whether (x, y) is inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// Widget renders to screen and may consume events. Layout (position/size)
// is owned by the parent, not the widget, so widgets stay layout-agnostic.
type Widget interface {
	Draw(scr tcell.Screen, r Rect)
	HandleEvent(ev tcell.Event) bool
}

// drawBoxBorder paints a single-line border around box using color.
func drawBoxBorder(scr tcell.Screen, box Rect, color tcell.Color) {
	style := StyleDefault.Foreground(color)
	scr.SetContent(box.X, box.Y, '┌', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, style)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, style)
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, style)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, style)
	}
	for yy := box.Y + 1; yy < box.Y+box.H-1; yy++ {
		scr.SetContent(box.X, yy, '│', nil, style)
		scr.SetContent(box.X+box.W-1, yy, '│', nil, style)
	}
}

// drawString writes s at (x, y) with the given style. Returns the width used.
// Clips at maxW (cells). Grapheme/wide-char naive: one rune = one cell.
func drawString(scr tcell.Screen, x, y int, style tcell.Style, s string, maxW int) int {
	if maxW <= 0 {
		return 0
	}
	used := 0
	for _, r := range s {
		if used >= maxW {
			break
		}
		scr.SetContent(x+used, y, r, nil, style)
		used++
	}
	return used
}

// fillRow paints a row of the given width with spaces in the given style,
// starting at (x, y). Used for full-width backgrounds (status bar, header).
func fillRow(scr tcell.Screen, x, y, w int, style tcell.Style) {
	for i := 0; i < w; i++ {
		scr.SetContent(x+i, y, ' ', nil, style)
	}
}

// clearRect paints r with spaces in the default style.
func clearRect(scr tcell.Screen, r Rect) {
	for row := 0; row < r.H; row++ {
		fillRow(scr, r.X, r.Y+row, r.W, tcell.StyleDefault)
	}
}

// ColorDimOverlay is the full-screen fill applied by dimBackground behind
// modals. Use a dark gray (not a light gray like 251) so the terminal keeps
// contrast: a light overlay washes out white text on dark backgrounds.
var ColorDimOverlay = tcell.Color234

// dimBackground paints a darker background over every cell in the
// screen so the pane on top reads as a lifted panel. Cells are
// rewritten with their existing rune and foreground, only the
// background attribute changes. This is cheap because tcell already
// holds the cell buffer in memory.
func dimBackground(scr tcell.Screen) {
	w, h := scr.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, _, style, _ := scr.GetContent(x, y)
			fg, _, attr := style.Decompose()
			newStyle := tcell.StyleDefault.Foreground(dimForeground(fg)).Background(ColorDimOverlay).Attributes(attr)
			scr.SetContent(x, y, r, nil, newStyle)
		}
	}
}

// dimForeground softens a foreground tone so the dimmed backdrop does
// not force the eye to compete with unchanged bright text. Colors
// fall back to the muted palette entry; the default foreground and
// every defined color get nudged into the subtext band.
func dimForeground(fg tcell.Color) tcell.Color {
	if fg == tcell.ColorDefault {
		return ColorMuted
	}
	return ColorSubtext
}

// stripMarkup removes inline tview-style tags like [color:bg:flags] from s.
// Kept for backwards compatibility with strings that may still carry tags.
func stripMarkup(s string) string {
	out := make([]rune, 0, len(s))
	i := 0
	rs := []rune(s)
	for i < len(rs) {
		if rs[i] == '[' {
			end := -1
			for j := i + 1; j < len(rs); j++ {
				if rs[j] == ']' {
					end = j
					break
				}
			}
			if end >= 0 {
				i = end + 1
				continue
			}
		}
		out = append(out, rs[i])
		i++
	}
	return string(out)
}

// runeCount returns the rune length of s (cell width approximation).
func runeCount(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// min helper (Go 1.21+ has built-in; keep local copy for portability).
func imin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max helper.
func imax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// clamp returns v clipped to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// drawScrollbar paints a vertical scrollbar on column x from row y0 to y0+h-1.
// It draws nothing when all content fits (total <= visible).
//
//	visible: how many rows of content are currently on screen
//	total:   total number of rows
//	offset:  scroll offset in content rows (0-based)
//
// The thumb length is proportional to visible/total with a minimum of one row
// so tiny thumbs remain hittable. The thumb position reflects offset/total.
// Track character is a faint vertical bar. Thumb character is a solid block.
func drawScrollbar(scr tcell.Screen, x, y0, h, visible, total, offset int) {
	if h <= 0 || total <= visible {
		return
	}
	trackStyle := StyleDefault.Foreground(ColorBorder)
	thumbStyle := StyleDefault.Foreground(ColorAccent)

	// Draw the track first as a faint rule
	for i := 0; i < h; i++ {
		scr.SetContent(x, y0+i, '│', nil, trackStyle)
	}

	// Thumb size proportional to visible window
	thumbLen := h * visible / total
	if thumbLen < 1 {
		thumbLen = 1
	}
	if thumbLen > h {
		thumbLen = h
	}

	// Thumb position scales the offset to the remaining track length.
	maxOff := total - visible
	if maxOff < 1 {
		maxOff = 1
	}
	thumbStart := (h - thumbLen) * offset / maxOff
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart+thumbLen > h {
		thumbStart = h - thumbLen
	}

	for i := 0; i < thumbLen; i++ {
		scr.SetContent(x, y0+thumbStart+i, '█', nil, thumbStyle)
	}
}
