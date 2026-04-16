package ui

import (
	"fmt"

	"github.com/rivo/tview"
)

type Mode int

const (
	ModeBrowse Mode = iota
	ModeDetail
	ModeSearch
	ModeCompact
	ModeView
	ModeFilter
)

type StatusBar struct {
	*tview.Flex
	modeView *tview.TextView // left: mode badge
	keysView *tview.TextView // center: keybindings
	posView  *tview.TextView // right: position
}

func NewStatusBar() *StatusBar {
	modeView := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	keysView := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)
	posView := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)

	flex := tview.NewFlex().
		AddItem(modeView, 12, 0, false).
		AddItem(keysView, 0, 1, false).
		AddItem(posView, 20, 0, false)

	s := &StatusBar{Flex: flex, modeView: modeView, keysView: keysView, posView: posView}
	s.SetMode(ModeBrowse)
	return s
}

func (s *StatusBar) SetMode(mode Mode) {
	var badge, keys string
	switch mode {
	case ModeBrowse:
		badge = "[black:green:b] BROWSE [-:-:-]"
		keys = "↑↓ scroll | click/enter select | 1-5 sort | / filter | q quit"
	case ModeDetail:
		badge = "[black:blue:b] DETAIL [-:-:-]"
		keys = "r resume | v view | s search | d delete | f fork | n name | c compact | esc close"
	case ModeSearch:
		badge = "[black:yellow:b] SEARCH [-:-:-]"
		keys = "tab next | enter submit | esc cancel"
	case ModeCompact:
		badge = "[black:orange:b] COMPACT [-:-:-]"
		keys = "tab next | enter apply | d dry run | esc cancel"
	case ModeView:
		badge = "[black:darkcyan:b] VIEW [-:-:-]"
		keys = "↑↓/pgup/pgdn scroll | q/esc close"
	case ModeFilter:
		badge = "[black:purple:b] FILTER [-:-:-]"
		keys = "type to filter | enter confirm | esc clear"
	}

	s.modeView.Clear()
	fmt.Fprint(s.modeView, badge)
	s.keysView.Clear()
	fmt.Fprint(s.keysView, keys)
}

func (s *StatusBar) SetPosition(current, total int) {
	s.posView.Clear()
	if total > 0 {
		fmt.Fprintf(s.posView, "%d/%d", current, total)
	}
}
