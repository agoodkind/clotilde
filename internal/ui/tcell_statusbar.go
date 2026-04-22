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
	// LegendOverride lets overlays/panels supply a context-specific
	// legend while reusing the same fixed status bar location.
	LegendOverride []LegendAction
	// BridgeCount surfaces the number of active claude --remote-control
	// sessions. Rendered as a small RC×N badge on the right edge so
	// the user always sees how many of their sessions are exposed.
	BridgeCount int
}

// LegendProvider is implemented by overlays that want to customize
// the bottom status bar legend while they are the topmost pane.
type LegendProvider interface {
	StatusLegendActions() []LegendAction
}

type legendHint struct {
	key   string
	label string
}

// LegendAction is the allowed catalog of legend entries. All status-bar
// legends map from these enums so wording stays consistent across modes
// and overlays.
type LegendAction int

const (
	LegendMove LegendAction = iota
	LegendTopBottom
	LegendSelectOption
	LegendSelectDetail
	LegendFilter
	LegendNew
	LegendRefresh
	LegendHelp
	LegendQuit
	LegendSearch
	LegendView
	LegendCompact
	LegendFork
	LegendDelete
	LegendEditBasedir
	LegendClose
	LegendTypeFilter
	LegendConfirm
	LegendClear
	LegendNext
	LegendFocus
	LegendAdjust
	LegendSelect
	LegendScroll
	LegendYesConfirm
	LegendNoCancel
	LegendPreview
	LegendApply
	LegendUndo
)

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
	var actions []LegendAction
	switch m {
	case StatusBrowse:
		actions = []LegendAction{
			LegendMove, LegendTopBottom, LegendSelectOption,
			LegendSelectDetail, LegendFilter, LegendNew,
			LegendRefresh, LegendHelp, LegendQuit,
		}
	case StatusDetail:
		actions = []LegendAction{
			LegendSelectOption, LegendSearch, LegendView,
			LegendCompact, LegendFork, LegendDelete,
			LegendEditBasedir, LegendClose,
		}
	case StatusFilter:
		actions = []LegendAction{LegendTypeFilter, LegendConfirm, LegendClear}
	case StatusSearch:
		actions = []LegendAction{LegendNext, LegendSearch, LegendClose}
	case StatusCompact:
		actions = []LegendAction{LegendFocus, LegendAdjust, LegendSelect, LegendClose}
	case StatusView:
		actions = []LegendAction{LegendScroll, LegendClose}
	case StatusConfirm:
		actions = []LegendAction{LegendYesConfirm, LegendNoCancel}
	}
	return legendSegmentsFromActions(actions)
}

func legendSegmentsFromActions(actions []LegendAction) []TextSegment {
	barBg := StyleStatusBar
	keyStyle := barBg.Foreground(ColorText).Bold(true)
	labelStyle := barBg.Foreground(ColorMuted)

	var segs []TextSegment
	for i, action := range actions {
		hint, ok := legendHintForAction(action)
		if !ok {
			continue
		}
		if i > 0 {
			segs = append(segs, TextSegment{Text: "  ", Style: barBg})
		}
		segs = append(segs, TextSegment{Text: hint.key, Style: keyStyle})
		segs = append(segs, TextSegment{Text: " " + hint.label, Style: labelStyle})
	}
	return segs
}

func legendHintForAction(action LegendAction) (legendHint, bool) {
	switch action {
	case LegendMove:
		return legendHint{key: "j/k", label: "move"}, true
	case LegendTopBottom:
		return legendHint{key: "g/G", label: "top/bot"}, true
	case LegendSelectOption:
		return legendHint{key: "enter/O", label: "select option"}, true
	case LegendSelectDetail:
		return legendHint{key: "space", label: "select detail"}, true
	case LegendFilter:
		return legendHint{key: "/", label: "filter"}, true
	case LegendNew:
		return legendHint{key: "N", label: "new"}, true
	case LegendRefresh:
		return legendHint{key: "R", label: "refresh"}, true
	case LegendHelp:
		return legendHint{key: "?", label: "help"}, true
	case LegendQuit:
		return legendHint{key: "q", label: "quit"}, true
	case LegendSearch:
		return legendHint{key: "/", label: "search"}, true
	case LegendView:
		return legendHint{key: "v", label: "view"}, true
	case LegendCompact:
		return legendHint{key: "c", label: "compact"}, true
	case LegendFork:
		return legendHint{key: "f", label: "fork"}, true
	case LegendDelete:
		return legendHint{key: "d", label: "delete"}, true
	case LegendEditBasedir:
		return legendHint{key: "B", label: "edit basedir"}, true
	case LegendClose:
		return legendHint{key: "esc", label: "close"}, true
	case LegendTypeFilter:
		return legendHint{key: "type", label: "filter"}, true
	case LegendConfirm:
		return legendHint{key: "enter", label: "confirm"}, true
	case LegendClear:
		return legendHint{key: "esc", label: "clear"}, true
	case LegendNext:
		return legendHint{key: "tab", label: "next"}, true
	case LegendFocus:
		return legendHint{key: "↑↓", label: "focus"}, true
	case LegendAdjust:
		return legendHint{key: "←→", label: "adjust"}, true
	case LegendSelect:
		return legendHint{key: "enter/spc", label: "select"}, true
	case LegendScroll:
		return legendHint{key: "↑↓", label: "scroll"}, true
	case LegendYesConfirm:
		return legendHint{key: "y", label: "confirm"}, true
	case LegendNoCancel:
		return legendHint{key: "n/esc", label: "cancel"}, true
	case LegendPreview:
		return legendHint{key: "p", label: "preview"}, true
	case LegendApply:
		return legendHint{key: "a", label: "apply"}, true
	case LegendUndo:
		return legendHint{key: "u", label: "undo"}, true
	}
	return legendHint{}, false
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
	var segs []TextSegment
	if len(s.LegendOverride) == 0 {
		segs = legendFor(s.Mode)
	} else {
		segs = legendSegmentsFromActions(s.LegendOverride)
	}
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
