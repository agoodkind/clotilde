package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
)

// TviewSearchResult holds the outcome of the search form.
type TviewSearchResult struct {
	Session   *session.Session
	Query     string
	Depth     string
	Cancelled bool
}

// NewSearchFormOverlay creates a tview.Form for searching a session's conversation.
// The form is pre-filled with the given session.
// onDone is called with the result when the user submits or cancels.
func NewSearchFormOverlay(sess *session.Session, onDone func(TviewSearchResult)) tview.Primitive {
	form := tview.NewForm()

	sessionName := ""
	if sess != nil {
		sessionName = sess.Name
	}

	depths := []string{"quick", "normal", "deep", "extra-deep"}
	depthDescriptions := map[string]string{
		"quick":      "embedding similarity only (~2s)",
		"normal":     "embedding + LLM sweep (~30s)",
		"deep":       "sweep + rerank + analysis (~2-5min)",
		"extra-deep": "full pipeline, largest model (~10min+)",
	}

	selectedDepth := 0

	form.AddTextView("Session", sessionName, 40, 1, true, false)
	form.AddInputField("Query", "", 50, nil, nil)
	form.AddDropDown("Depth", []string{
		"quick - " + depthDescriptions["quick"],
		"normal - " + depthDescriptions["normal"],
		"deep - " + depthDescriptions["deep"],
		"extra-deep - " + depthDescriptions["extra-deep"],
	}, 0, func(option string, index int) {
		selectedDepth = index
	})

	form.AddButton("Search", func() {
		query := form.GetFormItemByLabel("Query").(*tview.InputField).GetText()
		if query == "" {
			return
		}
		onDone(TviewSearchResult{
			Session: sess,
			Query:   query,
			Depth:   depths[selectedDepth],
		})
	})

	form.AddButton("Cancel", func() {
		onDone(TviewSearchResult{Cancelled: true})
	})

	form.SetBorder(true).
		SetTitle(" SEARCH ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(ColorModeSearch)

	form.SetButtonsAlign(tview.AlignCenter)
	form.SetFieldBackgroundColor(tcell.ColorDarkSlateGray)

	// Esc cancels
	form.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyEscape {
			onDone(TviewSearchResult{Cancelled: true})
			return nil
		}
		return event
	})

	// Center the form in a modal-like layout
	modal := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 15, 0, true).
			AddItem(nil, 0, 1, false),
			60, 0, true).
		AddItem(nil, 0, 1, false)

	return modal
}
