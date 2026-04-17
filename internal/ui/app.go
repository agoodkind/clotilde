// Package ui implements the clotilde TUI using raw tcell.
//
// Architecture:
//   - One tcell.Screen owns the terminal.
//   - One event loop reads screen.PollEvent and dispatches events.
//   - Widgets (table, textbox, statusbar, modal, input) implement a small
//     Widget interface. The App computes a Rect for each and calls Draw.
//   - Mouse hit testing is direct: the App checks which widget's Rect
//     contains the click coordinates. No InRect delegation chains.
//   - Terminal tab switches are handled via tcell.EventFocus. A full redraw
//     on refocus keeps the TUI responsive after switching terminal tabs.
package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// ---------------- Public API ----------------

// AppCallbacks provides hooks the TUI calls to perform actions.
// Kept identical to the previous tview API so cmd/root.go needs no changes.
type AppCallbacks struct {
	ResumeSession func(sess *session.Session) error
	DeleteSession func(sess *session.Session) error
	ForkSession   func(sess *session.Session) error
	RenameSession func(sess *session.Session) (string, error)
	StartSession  func() error
	ExtractDetail func(sess *session.Session) SessionDetail
	ExtractModel  func(sess *session.Session) string
	ViewContent   func(sess *session.Session) string
	Store         session.Store
}

// SessionDetail holds pre-extracted data for the details pane.
type SessionDetail struct {
	Model    string
	Messages []DetailMessage
}

// DetailMessage is a simplified message for display.
type DetailMessage struct {
	Role string
	Text string
}

// SortColumn identifies which column the table is sorted by.
type SortColumn int

const (
	SortColName SortColumn = iota
	SortColWorkspace
	SortColModel
	SortColCreated
	SortColUsed
)

// ---------------- App ----------------

// App is the main tcell TUI.
type App struct {
	screen tcell.Screen
	cb     AppCallbacks

	// Widgets
	table   *TableWidget
	details *DetailsView
	status  *StatusBarWidget

	// Overlays (one at a time)
	overlay Widget

	// Rects (recomputed on resize)
	headerRect Rect
	tableRect  Rect
	detailRect Rect
	statusRect Rect

	// Mode and state
	mode          StatusMode
	selected      *session.Session
	sessions      []*session.Session
	visibleIdx    []int // indexes into sessions after filter
	filter        string
	sortCol       SortColumn
	sortAsc       bool
	showEphemeral bool // when false (default), hide sessions from test/tmp workspaces
	hiddenCount   int  // number of sessions hidden by the ephemeral filter

	// Caches
	statsCache map[string]*transcript.CompactQuickStats
	modelCache map[string]string

	// Double click tracking
	lastClickTime time.Time
	lastClickRow  int

	// Event loop control
	running bool

	// Scroll position readout for status bar
	positionText string
}

// NewApp creates and returns the clotilde TUI.
func NewApp(sessions []*session.Session, cb AppCallbacks) *App {
	a := &App{
		cb:         cb,
		sessions:   sessions,
		mode:       StatusBrowse,
		statsCache: make(map[string]*transcript.CompactQuickStats),
		modelCache: make(map[string]string),
		sortCol:    SortColUsed,
		sortAsc:    false,
	}

	// Seed visible indexes with all sessions, unsorted for now.
	a.rebuildVisible()
	a.sortSessions()

	// Build widgets
	a.table = NewTableWidget([]string{"NAME", "WORKSPACE", "MODEL", "CREATED", "LAST USED"})
	a.table.SortCol = int(a.sortCol)
	a.table.SortAsc = a.sortAsc
	a.table.OnActivate = func(row int) { a.resumeRow(row) }
	a.table.OnSelect = func(row int) {
		a.trackSelection(row)
	}
	a.details = NewDetailsView()
	a.status = &StatusBarWidget{Mode: StatusBrowse}

	a.populateTable()
	return a
}

// Run starts the event loop.
func (a *App) Run() error {
	if err := a.initScreen(); err != nil {
		return err
	}
	defer a.screen.Fini()

	a.running = true
	a.draw()

	for a.running {
		ev := a.screen.PollEvent()
		if ev == nil {
			// Screen finalized externally.
			return nil
		}
		a.handleEvent(ev)
		if a.running {
			a.draw()
		}
	}
	return nil
}

// initScreen allocates a tcell screen and enables mouse + focus.
func (a *App) initScreen() error {
	scr, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("tcell NewScreen: %w", err)
	}
	if err := scr.Init(); err != nil {
		return fmt.Errorf("tcell Init: %w", err)
	}
	scr.EnableMouse(tcell.MouseButtonEvents)
	scr.EnableFocus()
	scr.Clear()
	a.screen = scr
	return nil
}

// PreWarmStats kicks off background model + stats computation.
// Results are integrated into the caches. A redraw is triggered via PostEvent.
func (a *App) PreWarmStats() {
	go func() {
		for _, sess := range a.sessions {
			if a.cb.ExtractModel != nil {
				name := sess.Name
				model := a.cb.ExtractModel(sess)
				a.modelCache[name] = model
			}
		}
		a.asyncRefresh()

		for _, sess := range a.sessions {
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
		}
		a.asyncRefresh()
	}()
}

// asyncRefresh posts an event that triggers a redraw without blocking.
func (a *App) asyncRefresh() {
	if a.screen == nil {
		return
	}
	// tcell PostEvent is safe across goroutines.
	_ = a.screen.PostEvent(tcell.NewEventInterrupt(a))
}

// ---------------- Event dispatch ----------------

func (a *App) handleEvent(ev tcell.Event) {
	switch e := ev.(type) {
	case *tcell.EventResize:
		a.screen.Sync()
	case *tcell.EventFocus:
		if e.Focused {
			a.screen.Sync()
		}
	case *tcell.EventInterrupt:
		// Posted from background goroutines to request a redraw.
		a.populateTable()
	case *tcell.EventKey:
		a.handleKey(e)
	case *tcell.EventMouse:
		a.handleMouse(e)
	}
}

// handleKey dispatches keyboard events.
// Global shortcuts (Ctrl+C) always apply. Overlays take priority over widgets.
func (a *App) handleKey(e *tcell.EventKey) {
	// Global: Ctrl+C always quits, regardless of focus.
	if e.Key() == tcell.KeyCtrlC {
		a.running = false
		return
	}

	if a.overlay != nil {
		a.overlay.HandleEvent(e)
		return
	}

	// Mode-specific shortcuts that must fire before the table consumes keys.
	switch e.Key() {
	case tcell.KeyEscape:
		if a.selected != nil {
			a.deselect()
			return
		}
		a.running = false
		return
	case tcell.KeyRune:
		switch e.Rune() {
		case ' ':
			if len(a.visibleIdx) > 0 {
				a.table.Active = true
				a.openDetails(a.currentSession())
			}
			return
		case 'q':
			if a.selected != nil {
				a.deselect()
				return
			}
			a.running = false
			return
		case '/':
			a.openFilter()
			return
		case '1':
			a.toggleSort(SortColName)
			return
		case '2':
			a.toggleSort(SortColCreated)
			return
		case '3':
			a.toggleSort(SortColUsed)
			return
		case '4':
			a.toggleSort(SortColWorkspace)
			return
		case '5':
			a.toggleSort(SortColModel)
			return
		case 'N':
			a.newSession()
			return
		case 'H':
			a.showEphemeral = !a.showEphemeral
			a.rebuildVisible()
			a.populateTable()
			return
		case 'r':
			if a.selected != nil || a.table.Active {
				a.resumeRow(a.table.SelectedRow)
			}
			return
		case 'v':
			if a.selected != nil {
				a.viewSelected()
			}
			return
		case 's':
			if a.selected != nil {
				a.openSearchForm()
			}
			return
		case 'd':
			if a.selected != nil {
				a.openDeleteConfirm()
			}
			return
		case 'f':
			if a.selected != nil {
				a.doFork()
			}
			return
		case 'n':
			if a.selected != nil {
				a.doRename()
			}
			return
		case 'c':
			if a.selected != nil {
				a.openCompactForm()
			}
			return
		}
	}

	// Fall through: table handles navigation.
	a.table.HandleEvent(e)
}

// handleMouse dispatches mouse events via direct rect hit tests.
// Overlays take priority. No InRect chain.
func (a *App) handleMouse(e *tcell.EventMouse) {
	x, y := e.Position()
	btns := e.Buttons()

	if a.overlay != nil {
		a.overlay.HandleEvent(e)
		return
	}

	if a.tableRect.Contains(x, y) {
		// Wheel scroll
		if btns&tcell.WheelUp != 0 {
			a.table.ScrollUp(3)
			return
		}
		if btns&tcell.WheelDown != 0 {
			a.table.ScrollDown(3)
			return
		}
		// Left click / double click
		if btns&tcell.Button1 != 0 {
			// Header click sorts
			if y == a.tableRect.Y {
				col := a.table.ColAtX(x)
				if col >= 0 && col < 5 {
					a.toggleSort(SortColumn(col))
				}
				return
			}
			row := a.table.RowAtY(y)
			if row < 0 {
				return
			}
			now := e.When()
			isDouble := !a.lastClickTime.IsZero() &&
				now.Sub(a.lastClickTime) < 400*time.Millisecond &&
				a.lastClickRow == row
			a.lastClickTime = now
			a.lastClickRow = row

			if isDouble {
				a.resumeRow(row)
				return
			}
			a.table.SelectAt(row)
			a.openDetails(a.currentSession())
			return
		}
	}
}

// ---------------- Drawing ----------------

func (a *App) draw() {
	a.layout()

	// Header bar
	w, _ := a.screen.Size()
	fillRow(a.screen, 0, 0, w, StyleHeaderBar)
	left := fmt.Sprintf(" clotilde  %d sessions", len(a.visibleIdx))
	if a.hiddenCount > 0 {
		left += fmt.Sprintf("  (%d hidden, H to show)", a.hiddenCount)
	} else if a.showEphemeral {
		left += "  (showing test/tmp)"
	}
	if a.filter != "" {
		left += fmt.Sprintf("  (filter: %q)", a.filter)
	}
	drawString(a.screen, 0, 0, StyleHeaderBar.Bold(true), left, w)

	// Table
	a.table.Draw(a.screen, a.tableRect)

	// Details
	if a.selected != nil {
		a.details.Draw(a.screen, a.detailRect)
	}

	// Status bar
	a.status.Mode = a.mode
	a.status.Position = a.positionTextFor()
	a.status.Draw(a.screen, a.statusRect)

	// Overlay on top
	if a.overlay != nil {
		ov, _ := a.screen.Size()
		_ = ov
		full := Rect{X: 0, Y: 0, W: a.tableRect.W + a.tableRect.X*2, H: a.statusRect.Y}
		if full.W < 1 {
			w2, h2 := a.screen.Size()
			full = Rect{X: 0, Y: 0, W: w2, H: h2}
		}
		// Overlays compute their own center; we pass the full screen rect.
		ww, hh := a.screen.Size()
		a.overlay.Draw(a.screen, Rect{X: 0, Y: 0, W: ww, H: hh})
	}

	a.screen.Show()
}

func (a *App) layout() {
	w, h := a.screen.Size()
	a.headerRect = Rect{X: 0, Y: 0, W: w, H: 1}

	// Table takes the available width and hugs content height, with header.
	tableTop := 1
	statusH := 1
	statusY := h - statusH
	if statusY < 2 {
		statusY = 2
	}
	a.statusRect = Rect{X: 0, Y: statusY, W: w, H: statusH}

	detailH := 0
	if a.selected != nil {
		// Details pane is 12 rows or whatever fits, minimum 6.
		detailH = 12
		if detailH > h-tableTop-statusH-3 {
			detailH = imax(6, h-tableTop-statusH-3)
		}
	}

	// Table takes remaining vertical space above details + status.
	tableH := statusY - tableTop - detailH
	if tableH < 3 {
		tableH = 3
	}
	a.tableRect = Rect{X: 2, Y: tableTop, W: w - 4, H: tableH}

	if detailH > 0 {
		a.detailRect = Rect{X: 0, Y: a.tableRect.Y + a.tableRect.H, W: w, H: detailH}
	}
}

func (a *App) positionTextFor() string {
	n := len(a.visibleIdx)
	if n == 0 || !a.table.Active {
		return ""
	}
	row := a.table.SelectedRow
	if row <= 0 {
		return "Top"
	}
	if row >= n-1 {
		return "Bot"
	}
	return fmt.Sprintf("%d%%", (row*100)/imax(1, n-1))
}

// ---------------- Table population ----------------

// populateTable rebuilds the table rows from the current visible sessions.
func (a *App) populateTable() {
	rows := make([][]TableCell, 0, len(a.visibleIdx))
	for _, idx := range a.visibleIdx {
		sess := a.sessions[idx]
		rows = append(rows, a.rowFor(sess))
	}
	a.table.Rows = rows
	a.table.SortCol = int(a.sortCol)
	a.table.SortAsc = a.sortAsc
	// Clamp selection after any change.
	if a.table.SelectedRow >= len(rows) {
		a.table.SelectedRow = imax(0, len(rows)-1)
	}
}

func (a *App) rowFor(sess *session.Session) []TableCell {
	nameStyle := StyleDefault.Foreground(ColorText)
	if sess.Metadata.IsForkedSession {
		nameStyle = StyleDefault.Foreground(ColorFork)
	}
	model := a.modelCache[sess.Name]
	if model == "-" {
		model = ""
	}
	if model == "<synthetic>" {
		model = "synthetic"
	}
	modelStyle := StyleDefault.Foreground(ColorMuted)
	switch {
	case strings.Contains(model, "opus"):
		modelStyle = StyleDefault.Foreground(ColorModelOpus)
	case strings.Contains(model, "sonnet"):
		modelStyle = StyleDefault.Foreground(ColorModelSonnet)
	case strings.Contains(model, "haiku"):
		modelStyle = StyleDefault.Foreground(ColorModelHaiku)
	}
	subStyle := StyleSubtext
	// Dim ephemeral rows when they are being shown, so they are easy to ignore.
	if isEphemeralSession(sess) {
		dim := StyleDefault.Foreground(ColorMuted).Dim(true)
		nameStyle = dim
		modelStyle = dim
		subStyle = dim
	}
	return []TableCell{
		{Text: sess.Name, Style: nameStyle},
		{Text: shortPath(sess.Metadata.WorkspaceRoot), Style: subStyle},
		{Text: model, Style: modelStyle},
		{Text: sess.Metadata.Created.Format("Jan 02"), Style: subStyle},
		{Text: util.FormatRelativeTime(sess.Metadata.LastAccessed), Style: subStyle},
	}
}

// rebuildVisible computes a.visibleIdx from a.sessions + a.filter.
// Also updates a.hiddenCount based on the ephemeral filter.
func (a *App) rebuildVisible() {
	a.visibleIdx = a.visibleIdx[:0]
	a.hiddenCount = 0
	f := strings.ToLower(a.filter)
	for i, sess := range a.sessions {
		if !a.showEphemeral && isEphemeralSession(sess) {
			a.hiddenCount++
			continue
		}
		if f != "" {
			hay := strings.ToLower(sess.Name + " " + sess.Metadata.WorkspaceRoot + " " + sess.Metadata.Context)
			if !strings.Contains(hay, f) {
				continue
			}
		}
		a.visibleIdx = append(a.visibleIdx, i)
	}
}

// sortSessions sorts a.sessions in place by current sort column.
func (a *App) sortSessions() {
	sort.SliceStable(a.sessions, func(i, j int) bool {
		x, y := a.sessions[i], a.sessions[j]
		var less bool
		switch a.sortCol {
		case SortColName:
			less = strings.ToLower(x.Name) < strings.ToLower(y.Name)
		case SortColWorkspace:
			less = x.Metadata.WorkspaceRoot < y.Metadata.WorkspaceRoot
		case SortColModel:
			less = a.modelCache[x.Name] < a.modelCache[y.Name]
		case SortColCreated:
			less = x.Metadata.Created.Before(y.Metadata.Created)
		case SortColUsed:
			less = x.Metadata.LastAccessed.Before(y.Metadata.LastAccessed)
		}
		if !a.sortAsc {
			less = !less
		}
		return less
	})
	a.rebuildVisible()
}

func (a *App) toggleSort(col SortColumn) {
	if a.sortCol == col {
		a.sortAsc = !a.sortAsc
	} else {
		a.sortCol = col
		a.sortAsc = col == SortColName
	}
	a.sortSessions()
	a.populateTable()
}

// currentSession returns the session at the currently selected table row.
func (a *App) currentSession() *session.Session {
	if len(a.visibleIdx) == 0 || a.table.SelectedRow < 0 || a.table.SelectedRow >= len(a.visibleIdx) {
		return nil
	}
	return a.sessions[a.visibleIdx[a.table.SelectedRow]]
}

func (a *App) trackSelection(row int) {
	// Keep details in sync if currently shown.
	if a.selected != nil && row >= 0 && row < len(a.visibleIdx) {
		a.selected = a.sessions[a.visibleIdx[row]]
		a.populateDetails()
	}
}

// ---------------- Selection / details ----------------

func (a *App) openDetails(sess *session.Session) {
	if sess == nil {
		return
	}
	a.selected = sess
	a.mode = StatusDetail
	a.populateDetails()
}

func (a *App) deselect() {
	a.selected = nil
	a.mode = StatusBrowse
}

func (a *App) populateDetails() {
	if a.selected == nil {
		return
	}
	detail := SessionDetail{Model: a.modelCache[a.selected.Name]}
	if a.cb.ExtractDetail != nil {
		detail = a.cb.ExtractDetail(a.selected)
	}
	a.details.Set(a.selected, detail, a.statsCache)
}

// ---------------- Actions ----------------

func (a *App) resumeRow(row int) {
	if row < 0 || row >= len(a.visibleIdx) || a.cb.ResumeSession == nil {
		return
	}
	sess := a.sessions[a.visibleIdx[row]]
	a.suspendAndRun(func() {
		_ = a.cb.ResumeSession(sess)
	})
	a.refreshSessions()
}

func (a *App) newSession() {
	if a.cb.StartSession == nil {
		return
	}
	a.suspendAndRun(func() {
		_ = a.cb.StartSession()
	})
	a.refreshSessions()
}

func (a *App) viewSelected() {
	if a.selected == nil || a.cb.ViewContent == nil {
		return
	}
	content := a.cb.ViewContent(a.selected)
	if content == "" {
		return
	}
	tb := &TextBox{
		Title:      "Conversation: " + a.selected.Name,
		TitleStyle: StyleHeader,
		Wrap:       true,
		Focused:    true,
	}
	tb.SetLines(strings.Split(content, "\n"))

	ov := &ViewerOverlay{Box: tb, OnClose: a.closeOverlay}
	a.overlay = ov
	a.mode = StatusView
}

func (a *App) openDeleteConfirm() {
	if a.selected == nil || a.cb.DeleteSession == nil {
		return
	}
	sess := a.selected
	m := &Modal{
		Title: "Delete Session",
		Body:  fmt.Sprintf("Delete session %q?", sess.Name),
		Details: []string{
			"Session folder and metadata will be removed.",
			"Claude transcript will be deleted.",
		},
		Buttons:     []string{"Cancel", "Delete"},
		ActiveIndex: 0,
		Destructive: true,
		Shortcuts:   map[rune]int{'y': 1, 'Y': 1, 'n': 0, 'N': 0},
	}
	m.OnChoice = func(idx int) {
		a.closeOverlay()
		if idx == 1 {
			_ = a.cb.DeleteSession(sess)
			a.deselect()
			a.refreshSessions()
		}
	}
	a.overlay = m
	a.mode = StatusConfirm
}

func (a *App) openSearchForm() {
	// Minimal: prompt for query only, depth fixed at "quick".
	sess := a.selected
	if sess == nil {
		return
	}
	input := NewTextInput("Search " + sess.Name + ": ")
	input.OnCancel = a.closeOverlay
	input.OnSubmit = func(q string) {
		a.closeOverlay()
		// TODO: wire search once cmd-level search is callable through cb.
		_ = q
	}
	a.overlay = &InputOverlay{Input: input, Title: "Search"}
	a.mode = StatusSearch
}

func (a *App) openCompactForm() {
	sess := a.selected
	if sess == nil {
		return
	}
	m := &Modal{
		Title: "Compact: " + sess.Name,
		Body:  "Compaction form (tcell port). Tab switches, Enter applies, Esc cancels.",
		Details: []string{
			"This stub preserves keyboard flow while the rich form is reimplemented.",
		},
		Buttons:     []string{"Cancel", "Apply"},
		ActiveIndex: 0,
		Shortcuts:   map[rune]int{'y': 1, 'n': 0},
	}
	m.OnChoice = func(idx int) {
		a.closeOverlay()
		_ = idx
	}
	a.overlay = m
	a.mode = StatusCompact
}

func (a *App) openFilter() {
	input := NewTextInput("Filter: ")
	input.Text = a.filter
	input.CursorX = runeCount(a.filter)
	input.OnChange = func(s string) {
		a.filter = s
		a.rebuildVisible()
		a.populateTable()
	}
	input.OnSubmit = func(s string) {
		a.filter = s
		a.rebuildVisible()
		a.populateTable()
		a.closeOverlay()
	}
	input.OnCancel = func() {
		a.filter = ""
		a.rebuildVisible()
		a.populateTable()
		a.closeOverlay()
	}
	a.overlay = &InputOverlay{Input: input, Title: "Filter"}
	a.mode = StatusFilter
}

func (a *App) doFork() {
	// Stub: fork via callback if provided.
	if a.cb.ForkSession == nil || a.selected == nil {
		return
	}
	sess := a.selected
	a.suspendAndRun(func() { _ = a.cb.ForkSession(sess) })
	a.refreshSessions()
}

func (a *App) doRename() {
	// Stub: rename via callback if provided.
	if a.cb.RenameSession == nil || a.selected == nil {
		return
	}
	sess := a.selected
	_, _ = a.cb.RenameSession(sess)
	a.refreshSessions()
}

func (a *App) closeOverlay() {
	a.overlay = nil
	if a.selected != nil {
		a.mode = StatusDetail
	} else {
		a.mode = StatusBrowse
	}
}

// refreshSessions reloads from store and repopulates the table.
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
	a.populateTable()
	a.deselect()
}

// suspendAndRun shuts down the screen, runs fn (which may launch claude),
// then re-initializes the screen and repaints. This replaces tview's Suspend.
func (a *App) suspendAndRun(fn func()) {
	if a.screen == nil {
		fn()
		return
	}
	a.screen.Fini()
	fn()
	_ = a.initScreen()
	a.draw()
}

// ---------------- Helpers used by overlays ----------------

// ViewerOverlay is a full-screen textbox with an Esc/q close binding.
type ViewerOverlay struct {
	Box     *TextBox
	OnClose func()
}

func (v *ViewerOverlay) Draw(scr tcell.Screen, r Rect) {
	v.Box.Draw(scr, r)
	// Footer hint
	if r.H > 1 {
		hint := " q/esc close   ↑↓ scroll "
		drawString(scr, r.X+r.W-runeCount(hint), r.Y+r.H-1, StyleMuted, hint, r.W)
	}
}

func (v *ViewerOverlay) HandleEvent(ev tcell.Event) bool {
	if ek, ok := ev.(*tcell.EventKey); ok {
		if ek.Key() == tcell.KeyEscape || (ek.Key() == tcell.KeyRune && ek.Rune() == 'q') {
			if v.OnClose != nil {
				v.OnClose()
			}
			return true
		}
	}
	return v.Box.HandleEvent(ev)
}

// InputOverlay centers a single-line input with a title.
type InputOverlay struct {
	Title string
	Input *TextInput
	rect  Rect
}

func (i *InputOverlay) Draw(scr tcell.Screen, r Rect) {
	w := 60
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 5
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	i.rect = box

	// Clear behind the overlay
	clearRect(scr, box)
	// Border
	borderStyle := StyleDefault.Foreground(ColorBorder)
	scr.SetContent(box.X, box.Y, '┌', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, borderStyle)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, borderStyle)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, borderStyle)
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, borderStyle)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, borderStyle)
	}
	for y := box.Y + 1; y < box.Y+box.H-1; y++ {
		scr.SetContent(box.X, y, '│', nil, borderStyle)
		scr.SetContent(box.X+box.W-1, y, '│', nil, borderStyle)
	}
	// Title
	if i.Title != "" {
		drawString(scr, box.X+2, box.Y+1, StyleMuted, i.Title, box.W-4)
	}
	// Input
	i.Input.Draw(scr, Rect{X: box.X + 2, Y: box.Y + 2, W: box.W - 4, H: 1})
}

func (i *InputOverlay) HandleEvent(ev tcell.Event) bool {
	if em, ok := ev.(*tcell.EventMouse); ok {
		x, y := em.Position()
		if i.rect.Contains(x, y) {
			return true
		}
	}
	return i.Input.HandleEvent(ev)
}

// ---------------- Shared helpers ----------------

// isEphemeralSession reports whether a session looks like a leaked test
// artifact or something rooted in a temp directory. These sessions pollute
// the dashboard and almost always have no transcript or a `synthetic` model.
//
// Signals we look for in the workspace path:
//   - /private/var/folders/... or /var/folders/... (macOS temp)
//   - /tmp/... (Unix temp)
//   - anything containing "/ginkgo" (Go test framework scratch dirs)
//   - anything containing "/clotilde-" under a temp dir (our own tests)
func isEphemeralSession(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	ws := sess.Metadata.WorkspaceRoot
	if ws == "" {
		return false
	}
	tempPrefixes := []string{
		"/private/var/folders/",
		"/var/folders/",
		"/tmp/",
		"/private/tmp/",
	}
	for _, p := range tempPrefixes {
		if strings.HasPrefix(ws, p) {
			return true
		}
	}
	if strings.Contains(ws, "/ginkgo") {
		return true
	}
	return false
}

// shortPath abbreviates a workspace path for display.
func shortPath(root string) string {
	if root == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Base(root)
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}
