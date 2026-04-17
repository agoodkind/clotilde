package ui

import (
	"github.com/gdamore/tcell/v2"
)

// StatusMode identifies which mode badge and legend to show.
type StatusMode int

const (
	StatusBrowse StatusMode = iota
	StatusDetail
	StatusFilter
	StatusSearch
	StatusCompact
	StatusView
	StatusConfirm
)

// StatusBarWidget draws the single line at the bottom of the screen.
// It has a full width background, a colored mode badge on the left,
// contextual keybinding hints in the middle, and a scroll/position
// indicator on the right.
type StatusBarWidget struct {
	Mode     StatusMode
	Position string // e.g. "Top", "Bot", "45%". Empty means nothing to show.
	// BridgeCount surfaces the number of active claude --remote-control
	// sessions. Rendered as a small RC×N badge on the right edge so
	// the user always sees how many of their sessions are exposed.
	BridgeCount int
}

// badgeFor returns the label and background color for a mode.
func badgeFor(m StatusMode) (string, tcell.Color) {
	switch m {
	case StatusBrowse:
		return " BROWSE ", ColorModeBrowse
	case StatusDetail:
		return " DETAIL ", ColorModeDetail
	case StatusFilter:
		return " FILTER ", ColorModeFilter
	case StatusSearch:
		return " SEARCH ", ColorModeSearch
	case StatusCompact:
		return " COMPACT ", ColorModeCompact
	case StatusView:
		return " VIEW ", ColorModeView
	case StatusConfirm:
		return " CONFIRM ", ColorWarning
	}
	return " ? ", tcell.ColorWhite
}

// legendFor returns styled segments for the keybinding hints.
func legendFor(m StatusMode) []TextSegment {
	type kv struct{ key, label string }
	var pairs []kv
	switch m {
	case StatusBrowse:
		pairs = []kv{
			{"j/k", "move"}, {"g/G", "top/bot"}, {"enter", "options"},
			{"space", "detail"}, {"/", "filter"}, {"N", "new"},
			{"R", "refresh"}, {"?", "help"}, {"q", "quit"},
		}
	case StatusDetail:
		pairs = []kv{
			{"enter/O", "options"}, {"/", "search"}, {"v", "view"},
			{"c", "compact"}, {"f", "fork"}, {"d", "delete"},
			{"B", "basedir"}, {"esc", "close"},
		}
	case StatusFilter:
		pairs = []kv{{"type", "filter"}, {"enter", "confirm"}, {"esc", "clear"}}
	case StatusSearch:
		pairs = []kv{{"tab", "next"}, {"enter", "search"}, {"esc", "cancel"}}
	case StatusCompact:
		pairs = []kv{{"tab", "next"}, {"enter", "apply"}, {"esc", "cancel"}}
	case StatusView:
		pairs = []kv{{"↑↓", "scroll"}, {"q/esc", "close"}}
	case StatusConfirm:
		pairs = []kv{{"y", "confirm"}, {"n/esc", "cancel"}}
	}
	barBg := StyleStatusBar
	keyStyle := barBg.Foreground(ColorText).Bold(true)
	labelStyle := barBg.Foreground(ColorMuted)

	var segs []TextSegment
	for i, p := range pairs {
		if i > 0 {
			segs = append(segs, TextSegment{Text: "  ", Style: barBg})
		}
		segs = append(segs, TextSegment{Text: p.key, Style: keyStyle})
		segs = append(segs, TextSegment{Text: " " + p.label, Style: labelStyle})
	}
	return segs
}

// Draw renders the status bar into r (r.H should be 1).
func (s *StatusBarWidget) Draw(scr tcell.Screen, r Rect) {
	fillRow(scr, r.X, r.Y, r.W, StyleStatusBar)

	label, bg := badgeFor(s.Mode)
	badgeStyle := tcell.StyleDefault.Background(bg).Foreground(tcell.ColorBlack).Bold(true)

	x := r.X + 1
	used := drawString(scr, x, r.Y, badgeStyle, label, r.W-1)
	x += used
	// Small gap after badge
	drawString(scr, x, r.Y, StyleStatusBar, "  ", 2)
	x += 2

	// Legend
	segs := legendFor(s.Mode)
	for _, seg := range segs {
		if x >= r.X+r.W {
			break
		}
		u := drawString(scr, x, r.Y, seg.Style, seg.Text, r.X+r.W-x)
		x += u
	}

	// Right aligned bridge indicator. Sits to the left of the
	// position field when both are present.
	rightX := r.X + r.W
	if s.BridgeCount > 0 {
		txt := " RC×" + itoa(s.BridgeCount) + " "
		bx := rightX - runeCount(txt)
		if bx > x {
			drawString(scr, bx, r.Y, tcell.StyleDefault.Background(ColorSuccess).Foreground(tcell.ColorBlack).Bold(true), txt, rightX-bx)
			rightX = bx
		}
	}

	// Right aligned position
	if s.Position != "" {
		posStyle := StyleStatusBar.Foreground(ColorText).Bold(true)
		txt := " " + s.Position + " "
		rx := rightX - runeCount(txt)
		if rx > x {
			drawString(scr, rx, r.Y, posStyle, txt, rightX-rx)
		}
	}
}

// itoa is a small wrapper over strconv to keep imports tidy.
func itoa(n int) string {
	if n < 0 {
		return "?"
	}
	if n == 0 {
		return "0"
	}
	const digits = "0123456789"
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

// HandleEvent does nothing (status bar is decorative).
func (s *StatusBarWidget) HandleEvent(ev tcell.Event) bool { return false }
