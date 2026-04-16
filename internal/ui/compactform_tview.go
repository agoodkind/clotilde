package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// TviewCompactResult holds the outcome of the compact form.
type TviewCompactResult struct {
	BoundaryPercent  int
	StripToolResults bool
	StripThinking    bool
	StripLargeInputs bool
	Applied          bool
	DryRun           bool
	Cancelled        bool
}

// NewCompactFormOverlay creates the compact control panel.
func NewCompactFormOverlay(sess *session.Session, onDone func(TviewCompactResult)) tview.Primitive {
	form := tview.NewForm()

	// Load current stats
	var chainLen, tokens, compactions int
	if sess.Metadata.TranscriptPath != "" {
		if qs, err := transcript.QuickStats(sess.Metadata.TranscriptPath); err == nil {
			chainLen = qs.EntriesInContext
			tokens = qs.EstimatedTokens
			compactions = qs.Compactions
		}
	}

	// Current state display
	stateText := fmt.Sprintf("Chain: %s entries | Tokens: ~%s | Compactions: %d",
		fmtNumber(chainLen), fmtTokens(tokens), compactions)
	form.AddTextView("Current", stateText, 60, 1, true, false)

	// Boundary slider (as a number input for now)
	form.AddInputField("Boundary %", "50", 5, tview.InputFieldInteger, nil)

	// Strip options
	form.AddCheckbox("Strip tool results", false, nil)
	form.AddCheckbox("Strip thinking blocks", false, nil)
	form.AddCheckbox("Strip large inputs (>1KB)", false, nil)

	form.AddButton("Apply", func() {
		boundaryField := form.GetFormItemByLabel("Boundary %").(*tview.InputField).GetText()
		pct := 50
		fmt.Sscanf(boundaryField, "%d", &pct)

		onDone(TviewCompactResult{
			BoundaryPercent:  pct,
			StripToolResults: form.GetFormItemByLabel("Strip tool results").(*tview.Checkbox).IsChecked(),
			StripThinking:    form.GetFormItemByLabel("Strip thinking blocks").(*tview.Checkbox).IsChecked(),
			StripLargeInputs: form.GetFormItemByLabel("Strip large inputs (>1KB)").(*tview.Checkbox).IsChecked(),
			Applied:          true,
		})
	})

	form.AddButton("Dry Run", func() {
		boundaryField := form.GetFormItemByLabel("Boundary %").(*tview.InputField).GetText()
		pct := 50
		fmt.Sscanf(boundaryField, "%d", &pct)

		onDone(TviewCompactResult{
			BoundaryPercent:  pct,
			StripToolResults: form.GetFormItemByLabel("Strip tool results").(*tview.Checkbox).IsChecked(),
			StripThinking:    form.GetFormItemByLabel("Strip thinking blocks").(*tview.Checkbox).IsChecked(),
			StripLargeInputs: form.GetFormItemByLabel("Strip large inputs (>1KB)").(*tview.Checkbox).IsChecked(),
			DryRun:           true,
		})
	})

	form.AddButton("Cancel", func() {
		onDone(TviewCompactResult{Cancelled: true})
	})

	form.SetBorder(true).
		SetTitle(" COMPACT: " + sess.Name + " ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(ColorModeCompact)

	form.SetButtonsAlign(tview.AlignCenter)
	form.SetFieldBackgroundColor(tcell.ColorDarkSlateGray)

	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			onDone(TviewCompactResult{Cancelled: true})
			return nil
		}
		return event
	})

	// Center in modal layout
	modal := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 20, 0, true).
			AddItem(nil, 0, 1, false),
			70, 0, true).
		AddItem(nil, 0, 1, false)

	return modal
}
