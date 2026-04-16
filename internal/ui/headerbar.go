package ui

import (
	"fmt"

	"github.com/rivo/tview"
)

// HeaderBar shows clotilde branding + session count on the left,
// context-sensitive keybinding hints on the right.
type HeaderBar struct {
	*tview.Flex
	leftView  *tview.TextView
	rightView *tview.TextView
}

func NewHeaderBar() *HeaderBar {
	left := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignLeft)
	right := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignRight)

	flex := tview.NewFlex().
		AddItem(left, 0, 1, false).
		AddItem(right, 0, 1, false)

	h := &HeaderBar{Flex: flex, leftView: left, rightView: right}
	h.Update(0, 0, "")
	h.SetKeys(ModeBrowse)
	return h
}

func (h *HeaderBar) Update(sessions, forks int, selectedInfo string) {
	left := fmt.Sprintf("[::b]clotilde[-:-:-] [gray]|[-] %d sessions", sessions)
	if forks > 0 {
		left += fmt.Sprintf(" [gray]|[-] %d forks", forks)
	}
	h.leftView.Clear()
	fmt.Fprint(h.leftView, left)
}

func (h *HeaderBar) SetKeys(mode Mode) {
	var keys string
	switch mode {
	case ModeBrowse:
		keys = "[gray]↑↓ scroll  click/enter select  1-5 sort  / filter  q quit[-]"
	case ModeDetail:
		keys = "[gray]r resume  v view  s search  d delete  f fork  n name  c compact  esc close[-]"
	case ModeSearch:
		keys = "[gray]tab next  enter submit  esc cancel[-]"
	case ModeCompact:
		keys = "[gray]tab next  enter apply  d dry run  esc cancel[-]"
	case ModeView:
		keys = "[gray]↑↓ scroll  q/esc close[-]"
	case ModeFilter:
		keys = "[gray]type to filter  enter confirm  esc clear[-]"
	}
	h.rightView.Clear()
	fmt.Fprint(h.rightView, keys)
}
