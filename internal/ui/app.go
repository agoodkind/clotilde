package ui

import (
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// AppCallbacks provides the hooks the TUI calls to perform actions.
// This breaks the import cycle between ui and cmd/claude packages.
type AppCallbacks struct {
	// ResumeSession is called when the user presses 'r' on a selected session.
	// The TUI suspends, the callback runs Claude, and the TUI resumes after.
	ResumeSession func(sess *session.Session) error

	// DeleteSession is called after confirmation to delete a session.
	DeleteSession func(sess *session.Session) error

	// ForkSession is called to fork the selected session.
	ForkSession func(sess *session.Session) error

	// RenameSession is called to auto-name a session via LLM.
	RenameSession func(sess *session.Session) (string, error)

	// ExtractDetail extracts model and recent messages for the details pane.
	// This avoids the ui package importing the claude package.
	ExtractDetail func(sess *session.Session) SessionDetail

	// ExtractModel returns just the model string for a session (lighter than ExtractDetail).
	ExtractModel func(sess *session.Session) string

	// Store provides session operations.
	Store session.Store
}

// App is the main tview application for clotilde.
type App struct {
	app      *tview.Application
	pages    *tview.Pages // for modal overlays
	root     *tview.Flex  // header + table + details + status
	header   *HeaderBar
	table    *SessionTable
	details  *DetailsPane
	status   *StatusBar
	cb       AppCallbacks
	selected *session.Session // currently selected session (nil = nothing)
}

// NewApp creates the clotilde TUI application.
func NewApp(sessions []*session.Session, cb AppCallbacks) *App {
	a := &App{
		app:     tview.NewApplication(),
		pages:   tview.NewPages(),
		header:  NewHeaderBar(),
		table:   NewSessionTable(),
		details: NewDetailsPane(),
		status:  NewStatusBar(),
		cb:      cb,
	}

	// Root layout: header (1) + table (grows) + details (hidden) + status (2)
	a.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header, 1, 0, false).
		AddItem(a.table, 0, 1, true).
		AddItem(a.details, 0, 0, false). // starts hidden
		AddItem(a.status, 2, 0, false)

	// Pages: main content + modal overlays
	a.pages.AddPage("main", a.root, true, true)

	// Enable mouse
	a.app.EnableMouse(true)

	// Load sessions
	a.table.SetSessions(sessions)
	a.updateHeader()

	// Wire selection callback
	a.table.OnSelect = func(sess *session.Session) {
		a.selectSession(sess)
	}

	// Global key handler
	a.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		return a.handleGlobalKey(event)
	})

	a.app.SetRoot(a.pages, true)

	return a
}

// Run starts the TUI event loop. Blocks until quit.
func (a *App) Run() error {
	return a.app.Run()
}

// selectSession opens the details pane for a session.
func (a *App) selectSession(sess *session.Session) {
	a.selected = sess
	if sess == nil {
		// Close details pane
		a.root.ResizeItem(a.details, 0, 0)
		a.status.SetMode(ModeBrowse)
		a.app.SetFocus(a.table)
		return
	}

	// Extract detail data via callback (avoids import cycle)
	detail := SessionDetail{Model: "-"}
	if a.cb.ExtractDetail != nil {
		detail = a.cb.ExtractDetail(sess)
	}

	// Share stats cache
	a.details.SetStatsCache(a.table.StatsCache)
	a.details.ShowSession(sess, detail)

	// Open details pane (12 rows)
	a.root.ResizeItem(a.details, 12, 0)
	a.status.SetMode(ModeDetail)
	a.updateHeader()
}

// deselectSession closes the details pane.
func (a *App) deselectSession() {
	a.selected = nil
	a.root.ResizeItem(a.details, 0, 0)
	a.status.SetMode(ModeBrowse)
	a.updateHeader()
	a.app.SetFocus(a.table)
}

// updateHeader refreshes the header bar with current counts.
func (a *App) updateHeader() {
	total := a.table.TotalCount()
	forks := 0
	for _, s := range a.table.sessions {
		if s.Metadata.IsForkedSession {
			forks++
		}
	}
	info := ""
	if a.selected != nil {
		info = a.selected.Name
	}
	a.header.Update(total, forks, info)
}

// handleGlobalKey routes key events based on the current mode.
func (a *App) handleGlobalKey(event *tcell.EventKey) *tcell.EventKey {
	key := event.Key()

	// Esc always deselects / closes overlays
	if key == tcell.KeyEscape {
		if a.selected != nil {
			a.deselectSession()
			return nil
		}
	}

	// q quits from browse mode (not when in details or overlays)
	if key == tcell.KeyRune && event.Rune() == 'q' && a.selected == nil {
		a.app.Stop()
		return nil
	}

	// Tab switches focus between table and details
	if key == tcell.KeyTab && a.selected != nil {
		if a.app.GetFocus() == a.table.Table {
			a.app.SetFocus(a.details)
			a.status.SetMode(ModeDetail)
		} else {
			a.app.SetFocus(a.table)
			a.status.SetMode(ModeDetail)
		}
		return nil
	}

	// Sort keys (always available)
	if key == tcell.KeyRune {
		switch event.Rune() {
		case '1':
			a.table.ToggleSort(SortColName)
			return nil
		case '2':
			a.table.ToggleSort(SortColCreated)
			return nil
		case '3':
			a.table.ToggleSort(SortColUsed)
			return nil
		case '4':
			a.table.ToggleSort(SortColWorkspace)
			return nil
		case '5':
			a.table.ToggleSort(SortColModel)
			return nil
		}
	}

	// Filter
	if key == tcell.KeyRune && event.Rune() == '/' && a.selected == nil {
		a.showFilter()
		return nil
	}

	// Action shortcuts (only when session is selected)
	if a.selected != nil && key == tcell.KeyRune {
		switch event.Rune() {
		case 'r': // resume
			a.resumeSelected()
			return nil
		case 'v': // view conversation
			a.viewSelected()
			return nil
		case 's': // search
			a.searchSelected()
			return nil
		case 'd': // delete
			a.deleteSelected()
			return nil
		case 'f': // fork
			a.forkSelected()
			return nil
		case 'n': // rename
			a.renameSelected()
			return nil
		case 'c': // compact
			a.compactSelected()
			return nil
		}
	}

	return event
}

// resumeSelected suspends the TUI, runs Claude, then resumes.
func (a *App) resumeSelected() {
	sess := a.selected
	if sess == nil || a.cb.ResumeSession == nil {
		return
	}
	a.app.Suspend(func() {
		_ = a.cb.ResumeSession(sess)
	})
	// Refresh after resume
	a.refreshSessions()
}

// viewSelected shows the conversation viewer.
func (a *App) viewSelected() {
	// TODO: implement viewer overlay (#85)
}

// searchSelected shows the search form.
func (a *App) searchSelected() {
	// TODO: implement search overlay (#86)
}

// deleteSelected shows a confirmation modal.
func (a *App) deleteSelected() {
	// TODO: implement confirm modal (#88)
}

// forkSelected forks the selected session.
func (a *App) forkSelected() {
	// TODO: implement fork flow
}

// renameSelected auto-renames via LLM.
func (a *App) renameSelected() {
	// TODO: implement rename flow
}

// compactSelected shows the compact form.
func (a *App) compactSelected() {
	// TODO: implement compact overlay (#87)
}

// showFilter shows a filter input at the top of the table.
func (a *App) showFilter() {
	input := tview.NewInputField().
		SetLabel("Filter: ").
		SetFieldWidth(40)

	input.SetDoneFunc(func(key tcell.Key) {
		switch key {
		case tcell.KeyEnter:
			a.table.SetFilter(input.GetText())
			a.pages.RemovePage("filter")
			a.app.SetFocus(a.table)
			a.status.SetMode(ModeBrowse)
		case tcell.KeyEscape:
			a.table.SetFilter("")
			a.pages.RemovePage("filter")
			a.app.SetFocus(a.table)
			a.status.SetMode(ModeBrowse)
		}
	})

	input.SetChangedFunc(func(text string) {
		a.table.SetFilter(text)
	})

	// Show filter as a floating bar at the top
	a.pages.AddPage("filter", input, true, true)
	a.app.SetFocus(input)
	a.status.SetMode(ModeFilter)
}

// refreshSessions reloads the session list from the store.
func (a *App) refreshSessions() {
	if a.cb.Store == nil {
		return
	}
	sessions, err := a.cb.Store.List()
	if err != nil {
		return
	}
	a.table.SetSessions(sessions)
	a.updateHeader()

	// Pre-warm model cache and stats cache in background
	go func() {
		// Extract models first (fast: reads last few lines of transcript)
		if a.cb.ExtractModel != nil {
			for _, sess := range sessions {
				name := sess.Name
				if _, ok := a.table.ModelCache[name]; ok {
					continue
				}
				model := a.cb.ExtractModel(sess)
				a.app.QueueUpdateDraw(func() {
					a.table.ModelCache[name] = model
					a.table.render() // refresh to show model
				})
			}
		}

		// Then stats (slower: reads full transcript for tiktoken)
		for _, sess := range sessions {
			path := sess.Metadata.TranscriptPath
			if path == "" {
				continue
			}
			if _, ok := a.table.StatsCache[path]; ok {
				continue
			}
			if cached := transcript.LoadCachedStats(path); cached != nil {
				stats := cached.Stats
				a.app.QueueUpdateDraw(func() {
					a.table.StatsCache[path] = &stats
				})
				continue
			}
			qs, err := transcript.QuickStats(path)
			if err != nil {
				continue
			}
			if info, statErr := os.Stat(path); statErr == nil {
				transcript.SaveCachedStats(path, qs, info.ModTime())
			}
			qsCopy := qs
			a.app.QueueUpdateDraw(func() {
				a.table.StatsCache[path] = &qsCopy
			})
		}
	}()
}

// PreWarmStats starts background stats computation for all sessions.
func (a *App) PreWarmStats() {
	a.refreshSessions()
}

// FilterInput returns the filter input (helper for showing filter from outside).
func (a *App) FilterInput() *tview.InputField {
	return nil // filter is created dynamically in showFilter
}

// SelectedSession returns the currently selected session.
func (a *App) SelectedSession() *session.Session {
	return a.selected
}

// unused import guard
var _ = strings.TrimSpace
