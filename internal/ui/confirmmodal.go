package ui

import (
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// ConfirmResult carries the outcome of a confirmation dialog.
type ConfirmResult struct {
	Confirmed bool
}

// NewConfirmModal returns a modal confirmation dialog.
//
// title is shown as the modal border label.
// message is the main body text.
// details, if non-empty, are appended as bullet points below the message.
// destructive applies a red activated-button style to highlight the danger of confirming.
// onDone is called with true when the user confirms, false when they cancel.
//
// Two buttons are provided: "Cancel" (default focus, index 0) and "Confirm" (index 1).
// Keyboard shortcuts: y = confirm, n = cancel, Enter acts on the focused button.
//
// The caller adds the returned *tview.Modal to tview.Pages and removes it inside onDone.
func NewConfirmModal(title, message string, details []string, destructive bool, onDone func(confirmed bool)) *tview.Modal {
	var body strings.Builder
	body.WriteString(message)

	if len(details) > 0 {
		body.WriteString("\n")
		for _, d := range details {
			body.WriteString("\n• ")
			body.WriteString(d)
		}
	}

	modal := tview.NewModal().
		SetText(body.String()).
		AddButtons([]string{"Cancel", "Confirm"}).
		SetDoneFunc(func(_ int, buttonLabel string) {
			onDone(buttonLabel == "Confirm")
		})

	modal.SetTitle(" " + title + " ")
	modal.SetTitleAlign(tview.AlignCenter)

	// Default focus on "Cancel" (index 0). Safe default for destructive actions.
	modal.SetFocus(0)

	if destructive {
		// SetButtonActivatedStyle applies to whichever button currently has focus.
		// The Confirm button will display with a red background when the user tabs to it.
		modal.SetButtonActivatedStyle(
			tcell.StyleDefault.
				Background(tcell.ColorRed).
				Foreground(tcell.ColorWhite).
				Bold(true),
		)
	}

	// y/n shortcuts invoke the done callback directly.
	modal.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case 'y', 'Y':
				onDone(true)
				return nil
			case 'n', 'N':
				onDone(false)
				return nil
			}
		}
		return event
	})

	return modal
}
