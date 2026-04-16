// Package ui implements the clotilde TUI using tview/tcell.
//
// Architecture follows the k9s pattern:
//   - tview primitives used directly (Table, TextView, Flex)
//   - App-level SetInputCapture for global shortcuts (q, Esc, /, 1-5)
//   - Table-level SetInputCapture for table-specific keys (r, v, s, d, f, n, c)
//   - tview.Pages for modal overlays
//   - Focus explicitly managed: only the Table or a modal ever has focus
package ui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// AppCallbacks provides hooks the TUI calls to perform actions.
type AppCallbacks struct {
	ResumeSession func(sess *session.Session) error
	DeleteSession func(sess *session.Session) error
	ForkSession   func(sess *session.Session) error
	RenameSession func(sess *session.Session) (string, error)
	ExtractDetail func(sess *session.Session) SessionDetail
	ExtractModel  func(sess *session.Session) string
	Store         session.Store
}

// App is the main tview application.
type App struct {
	app     *tview.Application
	pages   *tview.Pages
	root    *tview.Flex // vertical: header(1) + table(grow) + details(0|12) + status(1)
	header  *tview.TextView
	table   *tview.Table
	details *DetailsPane
	status  *tview.TextView
	cb      AppCallbacks

	// State
	sessions   []*session.Session
	selected   *session.Session
	mode       Mode
	statsCache map[string]*transcript.CompactQuickStats
	modelCache map[string]string

	// Table state
	tableActive bool // false until first arrow/click activates selection
	sortCol     SortColumn
	sortAsc     bool
}

// NewApp creates and returns the clotilde TUI.
func NewApp(sessions []*session.Session, cb AppCallbacks) *App {
	a := &App{
		app:        tview.NewApplication(),
		pages:      tview.NewPages(),
		header:     tview.NewTextView().SetDynamicColors(true),
		table:      tview.NewTable(),
		details:    NewDetailsPane(),
		status:     tview.NewTextView().SetDynamicColors(true),
		cb:         cb,
		sessions:   sessions,
		mode:       ModeBrowse,
		statsCache: make(map[string]*transcript.CompactQuickStats),
		modelCache: make(map[string]string),
		sortCol:    SortColUsed,
		sortAsc:    false,
	}

	// Table setup: start NOT selectable (no highlight on load)
	a.table.
		SetBorders(false).
		SetSelectable(false, false).
		SetFixed(1, 0).
		SetSeparator(' ').
		SetSelectedStyle(tcell.StyleDefault.
			Background(ColorSelected).
			Foreground(ColorSelectedFg).
			Bold(true))

	// Table: highlight change just updates status (does NOT open details)
	a.table.SetSelectionChangedFunc(func(row, col int) {
		if row < 1 || !a.tableActive {
			return
		}
		a.updateStatus()
	})

	// Table: Enter resumes (only fires when selectable)
	a.table.SetSelectedFunc(func(row, col int) {
		if row < 1 || !a.tableActive {
			return
		}
		idx := row - 1
		if idx < len(a.sessions) {
			a.selected = a.sessions[idx]
			a.resumeSelected()
		}
	})

	// Table: mouse double-click resumes, header click sorts
	a.table.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftDoubleClick {
			row, _ := a.table.GetSelection()
			if row >= 1 && row-1 < len(a.sessions) {
				a.selected = a.sessions[row-1]
				a.resumeSelected()
			}
			return action, nil
		}
		if action == tview.MouseLeftClick {
			_, y := event.Position()
			if y > 0 && !a.tableActive {
				// Click on a data row activates the table
				a.tableActive = true
				a.table.SetSelectable(true, false)
			}
			if y == 0 { // header row click
				x, _ := event.Position()
				col := x / (a.termWidth() / 5) // approximate column
				if col >= 0 && col < 5 {
					a.toggleSort(SortColumn(col))
				}
				return action, nil
			}
		}
		return action, event
	})

	// Table-level key handler (fires when table has focus)
	a.table.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		// Activate table selection on first navigation key
		if !a.tableActive {
			activate := false
			switch event.Key() {
			case tcell.KeyUp, tcell.KeyDown:
				activate = true
			case tcell.KeyRune:
				if event.Rune() == 'j' || event.Rune() == 'k' {
					activate = true
				}
			}
			if activate {
				a.tableActive = true
				a.table.SetSelectable(true, false)
				a.table.Select(1, 0) // select first data row
				// Don't return nil: let the selection change callback fire
				return nil
			}
		}

		// Spacebar: open details pane and focus it
		if event.Key() == tcell.KeyRune && event.Rune() == ' ' && a.tableActive {
			row, _ := a.table.GetSelection()
			if row >= 1 && row-1 < len(a.sessions) {
				a.selectSession(a.sessions[row-1])
				a.app.SetFocus(a.details.leftCol) // focus details
			}
			return nil
		}

		if event.Key() == tcell.KeyRune {
			switch event.Rune() {
			case 'r':
				// Resume works from highlight OR from details
				if a.selected != nil {
					a.resumeSelected()
					return nil
				}
				if a.tableActive {
					row, _ := a.table.GetSelection()
					if row >= 1 && row-1 < len(a.sessions) {
						a.selected = a.sessions[row-1]
						a.resumeSelected()
						return nil
					}
				}
			case 'v':
				if a.selected != nil {
					a.viewSelected()
					return nil
				}
			case 's':
				if a.selected != nil {
					a.searchSelected()
					return nil
				}
			case 'd':
				if a.selected != nil {
					a.deleteSelected()
					return nil
				}
			case 'f':
				if a.selected != nil {
					a.forkSelected()
					return nil
				}
			case 'n':
				if a.selected != nil {
					a.renameSelected()
					return nil
				}
			case 'c':
				if a.selected != nil {
					a.compactSelected()
					return nil
				}
			case '/':
				a.showFilter()
				return nil
			case '1':
				a.toggleSort(SortColName)
				return nil
			case '2':
				a.toggleSort(SortColCreated)
				return nil
			case '3':
				a.toggleSort(SortColUsed)
				return nil
			case '4':
				a.toggleSort(SortColWorkspace)
				return nil
			case '5':
				a.toggleSort(SortColModel)
				return nil
			case 'q':
				if a.selected != nil {
					a.deselectSession()
					return nil
				}
				a.app.Stop()
				return nil
			}
		}
		if event.Key() == tcell.KeyEscape {
			if a.selected != nil {
				a.deselectSession()
				return nil
			}
			a.app.Stop()
			return nil
		}
		if event.Key() == tcell.KeyTab && a.selected != nil {
			a.app.SetFocus(a.details.leftCol)
			return nil
		}
		return event
	})

	// Details pane: Tab returns focus to table, Esc closes
	for _, tv := range []*tview.TextView{a.details.leftCol, a.details.rightCol} {
		tv := tv
		tv.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
			if event.Key() == tcell.KeyTab {
				a.app.SetFocus(a.table)
				return nil
			}
			if event.Key() == tcell.KeyEscape {
				a.deselectSession()
				a.app.SetFocus(a.table)
				return nil
			}
			if event.Key() == tcell.KeyRune && event.Rune() == 'q' {
				a.deselectSession()
				a.app.SetFocus(a.table)
				return nil
			}
			return event
		})
	}

	// Root layout
	a.root = tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(a.header, 1, 0, false).
		AddItem(a.table, 0, 1, true).
		AddItem(a.details, 0, 0, false). // hidden initially
		AddItem(a.status, 1, 0, false)

	a.pages.AddPage("main", a.root, true, true)

	// Mouse
	a.app.EnableMouse(true)

	// Populate
	a.renderTable()
	a.updateHeader()
	a.updateStatus()

	a.app.SetRoot(a.pages, true)
	a.app.SetFocus(a.table)

	return a
}

// Run starts the TUI event loop.
func (a *App) Run() error {
	return a.app.Run()
}

// PreWarmStats kicks off background model + stats computation.
func (a *App) PreWarmStats() {
	go func() {
		for _, sess := range a.sessions {
			// Models
			if a.cb.ExtractModel != nil {
				name := sess.Name
				model := a.cb.ExtractModel(sess)
				a.modelCache[name] = model
				a.app.QueueUpdateDraw(func() {
					a.renderTable()
				})
			}
		}
		for _, sess := range a.sessions {
			// Stats
			path := sess.Metadata.TranscriptPath
			if path == "" {
				continue
			}
			if cached := transcript.LoadCachedStats(path); cached != nil {
				stats := cached.Stats
				a.statsCache[path] = &stats
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
			a.statsCache[path] = &qsCopy
			a.app.QueueUpdateDraw(func() {
				if a.selected != nil {
					a.showDetails(a.selected)
				}
			})
		}
	}()
}

// --- Rendering ---

func (a *App) renderTable() {
	a.table.Clear()

	headers := []string{"NAME", "WORKSPACE", "MODEL", "CREATED", "LAST USED"}
	for col, h := range headers {
		indicator := ""
		if SortColumn(col) == a.sortCol {
			if a.sortAsc {
				indicator = " ^"
			} else {
				indicator = " v"
			}
		}
		expansions := []int{3, 2, 1, 1, 1} // NAME gets most space
		exp := 1
		if col < len(expansions) {
			exp = expansions[col]
		}
		a.table.SetCell(0, col, tview.NewTableCell(" "+h+indicator+" ").
			SetSelectable(false).
			SetTextColor(ColorText).
			SetBackgroundColor(ColorHeaderBg).
			SetAttributes(tcell.AttrBold).
			SetExpansion(exp))
	}

	for i, sess := range a.sessions {
		row := i + 1
		bg := ColorRowEven
		if i%2 == 1 {
			bg = ColorRowOdd
		}

		name := sess.Name
		if len(name) > 35 {
			name = name[:32] + "..."
		}
		nameColor := ColorText
		if sess.Metadata.IsForkedSession {
			nameColor = ColorFork
		}

		ws := shortPath(sess.Metadata.WorkspaceRoot)
		if len(ws) > 22 {
			ws = "..." + ws[len(ws)-19:]
		}

		model := a.modelCache[sess.Name]
		if model == "" || model == "-" {
			model = ""
		}
		if model == "<synthetic>" {
			model = "synthetic"
		}
		modelColor := ColorMuted
		if strings.Contains(model, "opus") {
			modelColor = ColorModelOpus
		} else if strings.Contains(model, "sonnet") {
			modelColor = ColorModelSonnet
		} else if strings.Contains(model, "haiku") {
			modelColor = ColorModelHaiku
		}

		created := sess.Metadata.Created.Format("Jan 02")
		lastUsed := util.FormatRelativeTime(sess.Metadata.LastAccessed)

		a.table.SetCell(row, 0, tview.NewTableCell(name).SetTextColor(nameColor).SetBackgroundColor(bg).SetExpansion(3))
		a.table.SetCell(row, 1, tview.NewTableCell(ws).SetTextColor(ColorSubtext).SetBackgroundColor(bg).SetExpansion(2))
		a.table.SetCell(row, 2, tview.NewTableCell(model).SetTextColor(modelColor).SetBackgroundColor(bg).SetExpansion(1))
		a.table.SetCell(row, 3, tview.NewTableCell(created).SetTextColor(ColorSubtext).SetBackgroundColor(bg).SetExpansion(1))
		a.table.SetCell(row, 4, tview.NewTableCell(lastUsed).SetTextColor(ColorSubtext).SetBackgroundColor(bg).SetExpansion(1))
	}
}

func (a *App) updateHeader() {
	a.header.Clear()
	forks := 0
	for _, s := range a.sessions {
		if s.Metadata.IsForkedSession {
			forks++
		}
	}
	left := "[::b]clotilde[-] [gray]|[-] " + fmtNumber(len(a.sessions)) + " sessions"
	if forks > 0 {
		left += " [gray]|[-] " + fmtNumber(forks) + " forks"
	}
	// Right side: keybindings (dimmed)
	var keys string
	switch a.mode {
	case ModeBrowse:
		keys = "[gray]↑↓ scroll  enter resume  1-5 sort  / filter  q quit[-]"
	case ModeDetail:
		keys = "[gray]r resume  v view  s search  d delete  f fork  n name  c compact  esc close[-]"
	default:
		keys = ""
	}
	// Pad between left and right
	padding := "                    " // will be trimmed by tview
	fmt.Fprint(a.header, left+padding+keys)
}

func (a *App) updateStatus() {
	a.status.Clear()
	var badge string
	switch a.mode {
	case ModeBrowse:
		badge = "[black:green:b] BROWSE [-:-:-]"
	case ModeDetail:
		badge = "[black:blue:b] DETAIL [-:-:-]"
	case ModeFilter:
		badge = "[black:purple:b] FILTER [-:-:-]"
	default:
		badge = "[black:white:b] " + string(rune(a.mode+'A')) + " [-:-:-]"
	}

	row, _ := a.table.GetSelection()
	total := len(a.sessions)
	pos := fmt.Sprintf("%d sessions", total)
	if a.tableActive && row > 0 {
		pct := 0
		if total > 0 {
			pct = row * 100 / total
		}
		pos = fmt.Sprintf("%d/%d  %d%%", row, total, pct)
	}

	fmt.Fprint(a.status, badge+"  "+pos)
}

// --- Selection ---

func (a *App) selectSession(sess *session.Session) {
	a.selected = sess
	a.mode = ModeDetail
	a.showDetails(sess)
	a.root.ResizeItem(a.details, 12, 0)
	a.updateHeader()
	a.updateStatus()
}

func (a *App) deselectSession() {
	a.selected = nil
	a.tableActive = false
	a.table.SetSelectable(false, false)
	a.mode = ModeBrowse
	a.root.ResizeItem(a.details, 0, 0)
	a.updateHeader()
	a.updateStatus()
}

func (a *App) showDetails(sess *session.Session) {
	detail := SessionDetail{Model: a.modelCache[sess.Name]}
	if a.cb.ExtractDetail != nil {
		detail = a.cb.ExtractDetail(sess)
	}
	a.details.SetStatsCache(a.statsCache)
	a.details.ShowSession(sess, detail)
}

// --- Actions ---

func (a *App) resumeSelected() {
	if a.selected == nil || a.cb.ResumeSession == nil {
		return
	}
	sess := a.selected
	a.app.Suspend(func() {
		_ = a.cb.ResumeSession(sess)
	})
	a.refreshSessions()
}

func (a *App) viewSelected() {
	if a.selected == nil {
		return
	}
	// TODO: load transcript, show in viewer overlay
}

func (a *App) searchSelected() {
	sess := a.selected
	if sess == nil {
		return
	}
	overlay := NewSearchFormOverlay(sess, func(result TviewSearchResult) {
		a.pages.RemovePage("search")
		a.app.SetFocus(a.table)
		a.mode = ModeBrowse
		a.updateHeader()
		a.updateStatus()
		if result.Cancelled {
			return
		}
		// TODO: run search, show results in viewer
	})
	a.pages.AddPage("search", overlay, true, true)
	a.mode = ModeSearch
	a.updateHeader()
	a.updateStatus()
}

func (a *App) deleteSelected() {
	sess := a.selected
	if sess == nil || a.cb.DeleteSession == nil {
		return
	}
	modal := NewConfirmModal(
		"Delete Session",
		"Delete session '"+sess.Name+"'?",
		[]string{"Session folder and metadata will be removed", "Claude transcript will be deleted"},
		true,
		func(confirmed bool) {
			a.pages.RemovePage("confirm")
			a.app.SetFocus(a.table)
			if confirmed {
				_ = a.cb.DeleteSession(sess)
				a.deselectSession()
				a.refreshSessions()
			}
		},
	)
	a.pages.AddPage("confirm", modal, true, true)
}

func (a *App) forkSelected()    {} // TODO
func (a *App) renameSelected()  {} // TODO
func (a *App) compactSelected() {} // TODO

func (a *App) showFilter() {
	input := tview.NewInputField().
		SetLabel("Filter: ").
		SetFieldWidth(40)

	input.SetDoneFunc(func(key tcell.Key) {
		a.pages.RemovePage("filter")
		a.app.SetFocus(a.table)
		a.mode = ModeBrowse
		a.updateHeader()
		a.updateStatus()
	})

	input.SetChangedFunc(func(text string) {
		// TODO: filter sessions
		_ = text
	})

	a.pages.AddPage("filter", input, true, true)
	a.app.SetFocus(input)
	a.mode = ModeFilter
	a.updateHeader()
	a.updateStatus()
}

func (a *App) toggleSort(col SortColumn) {
	if a.sortCol == col {
		a.sortAsc = !a.sortAsc
	} else {
		a.sortCol = col
		a.sortAsc = col == SortColName
	}
	a.sortSessions()
	a.renderTable()
}

func (a *App) sortSessions() {
	sortSessionSlice(a.sessions, a.sortCol, a.sortAsc)
}

func (a *App) refreshSessions() {
	if a.cb.Store == nil {
		return
	}
	sessions, err := a.cb.Store.List()
	if err != nil {
		return
	}
	a.sessions = sessions
	a.sortSessions()
	a.renderTable()
	a.updateHeader()
	a.updateStatus()
	a.deselectSession()
}

func (a *App) termWidth() int {
	_, _, w, _ := a.table.GetInnerRect()
	if w <= 0 {
		return 120
	}
	return w
}

// sortSessionSlice sorts sessions in place by the given column and direction.
func sortSessionSlice(sessions []*session.Session, col SortColumn, asc bool) {
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		var less bool
		switch col {
		case SortColName:
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case SortColWorkspace:
			less = a.Metadata.WorkspaceRoot < b.Metadata.WorkspaceRoot
		case SortColModel:
			less = a.Name < b.Name
		case SortColCreated:
			less = a.Metadata.Created.Before(b.Metadata.Created)
		case SortColUsed:
			less = a.Metadata.LastAccessed.Before(b.Metadata.LastAccessed)
		}
		if !asc {
			less = !less
		}
		return less
	})
}

// unused import guards
var _ = strings.TrimSpace
var _ = fmt.Sprintf
