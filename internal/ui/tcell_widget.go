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
