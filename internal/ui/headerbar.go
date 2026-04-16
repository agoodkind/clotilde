package ui

import (
	"fmt"

	"github.com/rivo/tview"
)

type HeaderBar struct {
	*tview.TextView
	sessionCount int
	forkCount    int
	selectedInfo string // context usage for selected session
}

func NewHeaderBar() *HeaderBar {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetTextAlign(tview.AlignLeft)
	h := &HeaderBar{TextView: tv}
	h.Update(0, 0, "")
	return h
}

func (h *HeaderBar) Update(sessions, forks int, selectedInfo string) {
	h.sessionCount = sessions
	h.forkCount = forks
	h.selectedInfo = selectedInfo

	left := fmt.Sprintf("[#00D7D7::b]clotilde[-:-:-] | %d sessions", sessions)
	if forks > 0 {
		left += fmt.Sprintf(" | [yellow]%d forks[-]", forks)
	}

	h.Clear()
	fmt.Fprintf(h, "%s", left)
}
