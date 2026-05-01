package ui

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
)

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

// drawString writes s at (x, y) with the given style. Returns the display
// width used (in terminal cells), clipping at maxW. Uses grapheme clusters
// and go-runewidth so East Asian and emoji line up with layout in cellCount.
func drawString(scr tcell.Screen, x, y int, style tcell.Style, s string, maxW int) int {
	if maxW <= 0 {
		return 0
	}
	gr := uniseg.NewGraphemes(s)
	used := 0
	for gr.Next() {
		cl := gr.Str()
		w := runewidth.StringWidth(cl)
		if w < 1 {
			rns := []rune(cl)
			if len(rns) == 0 {
				continue
			}
			w = runewidth.RuneWidth(rns[0])
		}
		if w < 1 {
			continue
		}
		if used+w > maxW {
			break
		}
		rns := []rune(cl)
		if len(rns) == 0 {
			used += w
			continue
		}
		scr.SetContent(x+used, y, rns[0], rns[1:], style)
		used += w
	}
	return used
}

// fillRow paints a row of the given width with spaces in the given style,
// starting at (x, y). Used for full-width backgrounds (status bar, header).
func fillRow(scr tcell.Screen, x, y, w int, style tcell.Style) {
	for i := range w {
		scr.SetContent(x+i, y, ' ', nil, style)
	}
}

// clearRect paints r with spaces in the default style.
func clearRect(scr tcell.Screen, r Rect) {
	for row := range r.H {
		fillRow(scr, r.X, r.Y+row, r.W, tcell.StyleDefault)
	}
}

type terminalTheme int

const (
	terminalThemeDark terminalTheme = iota
	terminalThemeLight
)

// dimBackground paints a darker background over every cell in the
// screen so the pane on top reads as a lifted panel. Cells are
// rewritten with their existing rune and foreground, only the
// background attribute changes. This is cheap because tcell already
// holds the cell buffer in memory.
func dimBackground(scr tcell.Screen) {
	w, h := scr.Size()
	backdrop := dimOverlayColor()
	for y := range h {
		for x := range w {
			str, style, _ := scr.Get(x, y)
			fg, _, attr := style.Decompose()
			newStyle := tcell.StyleDefault.Foreground(dimForeground(fg)).Background(backdrop).Attributes(attr)
			scr.Put(x, y, str, newStyle)
		}
	}
}

func dimOverlayColor() tcell.Color {
	return ColorDimOverlay
}

// dimForeground softens a foreground tone so the dimmed backdrop does
// not force the eye to compete with unchanged bright text. Colors
// fall back to the muted palette entry; the default foreground and
// every defined color get nudged into the subtext band.
func dimForeground(fg tcell.Color) tcell.Color {
	return ColorMuted
}

func detectTerminalTheme() terminalTheme {
	return terminalThemeFromSignals(os.Getenv("CLYDE_TUI_THEME"), os.Getenv("COLORFGBG"), macOSAppearance())
}

func terminalThemeFromSignals(explicit, colorfgbg, appleAppearance string) terminalTheme {
	switch strings.ToLower(strings.TrimSpace(explicit)) {
	case "light":
		return terminalThemeLight
	case "dark":
		return terminalThemeDark
	}
	if theme, ok := terminalThemeFromColorFGBG(colorfgbg); ok {
		return theme
	}
	if !strings.EqualFold(strings.TrimSpace(appleAppearance), "dark") && strings.TrimSpace(appleAppearance) != "" {
		return terminalThemeLight
	}
	return terminalThemeDark
}

func terminalThemeFromColorFGBG(value string) (terminalTheme, bool) {
	parts := strings.Split(strings.TrimSpace(value), ";")
	if len(parts) == 0 {
		return terminalThemeDark, false
	}
	bgText := strings.TrimSpace(parts[len(parts)-1])
	if bgText == "" {
		return terminalThemeDark, false
	}
	bg, err := strconv.Atoi(bgText)
	if err != nil {
		return terminalThemeDark, false
	}
	switch {
	case bg == 7 || bg == 15 || bg >= 230:
		return terminalThemeLight, true
	default:
		return terminalThemeDark, true
	}
}

func macOSAppearance() string {
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("defaults", "read", "-g", "AppleInterfaceStyle").Output()
	if err != nil {
		return "light"
	}
	return strings.TrimSpace(string(out))
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

// cellCount returns the display width of s in terminal cells.
func cellCount(s string) int {
	return runewidth.StringWidth(s)
}

// runeCount is an alias for cellCount. Kept for call sites that mean
// "layout width" in screen cells, not Go rune count.
func runeCount(s string) int {
	return cellCount(s)
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
	for i := range h {
		scr.SetContent(x, y0+i, '│', nil, trackStyle)
	}

	// Thumb size proportional to visible window
	thumbLen := max(h*visible/total, 1)
	thumbLen = min(thumbLen, h)

	// Thumb position scales the offset to the remaining track length.
	maxOff := max(total-visible, 1)
	thumbStart := max((h-thumbLen)*offset/maxOff, 0)
	if thumbStart+thumbLen > h {
		thumbStart = h - thumbLen
	}

	for i := range thumbLen {
		scr.SetContent(x, y0+thumbStart+i, '█', nil, thumbStyle)
	}
}
