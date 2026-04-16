package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ViewerOverlay wraps a tview.TextView for displaying conversation text.
// It is a vertical Flex: header (1 row) + scrollable TextView + footer (1 row).
// The caller adds this to tview.Pages and removes it on close.
type ViewerOverlay struct {
	*tview.Flex
	textView *tview.TextView
	title    string
}

// NewViewerOverlay creates a full-screen conversation viewer overlay.
// title is shown in bold in the header; content is the text body.
func NewViewerOverlay(title, content string) *ViewerOverlay {
	v := &ViewerOverlay{title: title}

	// Header: bold title, left-aligned.
	header := tview.NewTextView().
		SetDynamicColors(true).
		SetText(fmt.Sprintf("[::b]%s[-]", tview.Escape(title)))
	header.SetBackgroundColor(tcell.ColorDefault)

	// Content: scrollable, dynamic colors.
	v.textView = tview.NewTextView().
		SetScrollable(true).
		SetDynamicColors(true).
		SetText(content)
	v.textView.SetBackgroundColor(tcell.ColorDefault)

	// Footer: keybindings + scroll percentage.
	footer := tview.NewTextView().SetDynamicColors(true)
	footer.SetBackgroundColor(tcell.ColorDefault)

	refreshFooter := func() {
		_, _, _, height := v.textView.GetInnerRect()
		scrollRow, _ := v.textView.GetScrollOffset()
		total := countLines(content)
		var pctVal int
		if total <= height || height <= 0 {
			pctVal = 100
		} else {
			pctVal = scrollRow * 100 / (total - height)
			if pctVal > 100 {
				pctVal = 100
			}
		}
		footer.SetText(fmt.Sprintf(
			"[::d]q/Esc close | ↑↓/PgUp/PgDn scroll | %d%%[-]",
			pctVal,
		))
	}

	refreshFooter()

	// Update footer on scroll.
	v.textView.SetChangedFunc(func() {
		refreshFooter()
	})
	v.textView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() { //nolint:exhaustive
		case tcell.KeyUp, tcell.KeyDown,
			tcell.KeyPgUp, tcell.KeyPgDn,
			tcell.KeyHome, tcell.KeyEnd:
			// Let tview handle scrolling, then refresh footer.
			go refreshFooter()
		}
		return event
	})

	// Outer Flex: vertical stack.
	v.Flex = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(v.textView, 0, 1, true).
		AddItem(footer, 1, 0, false)
	v.Flex.SetBackgroundColor(tcell.ColorDefault)

	return v
}

// countLines counts the number of newline-separated lines in s.
func countLines(s string) int {
	n := 1
	for _, c := range s {
		if c == '\n' {
			n++
		}
	}
	return n
}
