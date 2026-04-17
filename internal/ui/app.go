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
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// ---------------- Public API ----------------

// AppOptions tweaks the startup behavior of the main TUI. Every field is
// optional. Zero values preserve the normal dashboard flow.
type AppOptions struct {
	// ReturnTo, when non-nil, pre-selects the given session in the table.
	// The header banner prompts the user to resume or pick something else.
	ReturnTo *session.Session
}

// AppCallbacks provides hooks the TUI calls to perform actions.
// Kept identical to the previous tview API so cmd/root.go needs no changes.
type AppCallbacks struct {
	ResumeSession func(sess *session.Session) error
	DeleteSession func(sess *session.Session) error
	ForkSession   func(sess *session.Session) error
	RenameSession func(sess *session.Session) (string, error)
	StartSession  func() error
	// StartSessionWithBasedir creates a new session pinned to the given
	// workspace root. Empty string falls back to the caller's cwd.
	StartSessionWithBasedir func(basedir string) error
	ApplyCompact  func(sess *session.Session, choices CompactChoices) (CompactResult, error)
	// SetBasedir rewrites the session's workspaceRoot field in metadata.
	// newPath is already resolved by the caller; "" clears the field.
	SetBasedir func(sess *session.Session, newPath string) error
	// RefreshSummary triggers a background regeneration of the session's
	// Context field via the daemon. It should return quickly once the
	// request is queued. The returned sessions callback (may be nil)
	// fires once the updated metadata is persisted; the TUI uses it to
	// redraw the affected row.
	RefreshSummary func(sess *session.Session, onDone func(*session.Session)) error
	ExtractDetail func(sess *session.Session) SessionDetail
	ExtractModel  func(sess *session.Session) string
	ViewContent   func(sess *session.Session) string
	Store         session.Store
	// SubscribeRegistry, when set, opens a long-lived subscription to
	// the daemon's registry-event stream. Each event nudges the TUI to
	// reload sessions from disk so adoptions land immediately instead
	// of waiting for the polling watcher. The returned cancel function
	// runs when the TUI exits. Errors are silently tolerated: the
	// fallback poller still runs.
	SubscribeRegistry func() (events <-chan RegistryEvent, cancel func(), err error)
}

// RegistryEvent is the UI-facing copy of the daemon RegistryEvent. The
// ui package keeps its own type so the daemon's protobuf does not leak
// into widget code.
type RegistryEvent struct {
	Kind        string
	SessionName string
	SessionID   string
}

// CompactResult is the in-TUI summary of a finished compaction. The
// applier fills it from the underlying transcript pipeline so the
// dashboard can show what the user just changed without printing to
// stdout.
type CompactResult struct {
	BackupPath        string
	BeforeBytes       int64
	AfterBytes        int64
	BeforeChainLines  int
	AfterChainLines   int
	BoundaryMoved     bool
	StrippedTotal     int
	StrippedImages    int
	StrippedTools     int
	StrippedThinking  int
	StrippedLargeIn   int
	KeptLastImages    int
	KeptLastTools     int
	KeptLastThinking  int
}

// SessionDetail holds pre-extracted data for the details pane.
// Messages is the most-recent short list used in the original design;
// AllMessages carries the full transcript for the scrollable right pane.
// Tools ranks the top assistant tool uses for the stats pane.
type SessionDetail struct {
	Model       string
	Messages    []DetailMessage // last N for quick peek (kept for backwards compat)
	AllMessages []DetailMessage // full transcript, ordered oldest -> newest
	Tools       []ToolUse       // descending by Count
}

// DetailMessage is a simplified message for display.
type DetailMessage struct {
	Role      string    // "user" or "assistant"
	Text      string
	Timestamp time.Time // zero when unknown
}

// ToolUse is a tool name and usage count inside a session.
type ToolUse struct {
	Name  string
	Count int
}

// scrollGrab identifies which scrollbar the user is currently dragging.
type scrollGrab int

const (
	grabNone scrollGrab = iota
	grabTable
	grabDetailsLeft
	grabDetailsRight
)

// SortColumn identifies which column the table is sorted by.
type SortColumn int

const (
	SortColName SortColumn = iota
	SortColWorkspace
	SortColModel
	SortColMessages
	SortColSummary
	SortColUsed
	SortColCreated
)

// ---------------- App ----------------

// App is the main tcell TUI.
type App struct {
	screen tcell.Screen
	cb     AppCallbacks

	// Widgets
	tabs    *TabBarWidget
	table   *TableWidget
	details *DetailsView
	status  *StatusBarWidget

	// activeTab indexes into tabs.Tabs. 0 is the sessions dashboard. 1
	// is the settings editor stub. The dashboard renders the table view
	// when activeTab is 0; other indices replace the body with a tab
	// specific panel.
	activeTab int

	// Overlays (one at a time)
	overlay Widget
	// overlayStack remembers overlays under the current one. Pushing a
	// new overlay on top (e.g. the compact result modal over the
	// compact form) preserves the bottom layer so dismissing the top
	// returns the user where they came from instead of dropping them
	// to the dashboard.
	overlayStack []Widget

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
	// exactTokenCache stores Claude API token counts keyed by transcript
	// path. Populated by a background goroutine when ANTHROPIC_API_KEY is
	// set. When a path has an entry the display switches off the "~"
	// approximate prefix.
	exactTokenCache map[string]int
	exactTokenMu    sync.Mutex

	// detailCache stores the fully-extracted SessionDetail keyed by session
	// name. Populated off the UI goroutine by loadDetailAsync so repeat
	// selections render instantly. detailLoading tracks sessions whose
	// load is in flight, guarding against duplicate goroutines.
	detailCache   map[string]SessionDetail
	detailLoading map[string]bool
	detailMu      sync.Mutex

	// spinnerFrame increments on each redraw that is waiting for async
	// data so the user sees motion in the details header.
	spinnerFrame int

	// summaryRefreshing tracks session names whose summary refresh is
	// in flight, so repeated highlights do not spawn duplicate requests.
	summaryRefreshing map[string]bool

	// lastInteraction records the last time a keyboard or mouse event
	// was handled. The background session watcher consults it and skips
	// a soft refresh when the user is mid-scroll or mid-click. The
	// value is read and written only from the event-loop goroutine
	// except for the watcher, which treats it as best-effort.
	lastInteraction time.Time
	interactionMu   sync.Mutex

	// storeSnapshot is a fingerprint of the session store from the most
	// recent watcher tick. When a newer snapshot differs, the watcher
	// posts a refresh interrupt. Keeping the fingerprint avoids calling
	// Store.List() twice when nothing changed.
	storeSnapshot string

	// Double click tracking
	lastClickTime time.Time
	lastClickRow  int

	// Scrollbar grab: which bar the user is currently dragging. Cleared
	// when any button release happens (buttons == 0).
	grab scrollGrab

	// Event loop control
	running bool

	// Scroll position readout for status bar
	positionText string

	// Post-session return banner. When non-empty the header shows a prompt
	// that invites the user to resume the named session with Enter.
	returnBanner string
}

// NewApp creates and returns the clotilde TUI.
func NewApp(sessions []*session.Session, cb AppCallbacks, opts ...AppOptions) *App {
	var opt AppOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	a := &App{
		cb:            cb,
		sessions:      sessions,
		mode:          StatusBrowse,
		statsCache:      make(map[string]*transcript.CompactQuickStats),
		modelCache:      make(map[string]string),
		exactTokenCache: make(map[string]int),
		detailCache:       make(map[string]SessionDetail),
		detailLoading:     make(map[string]bool),
		summaryRefreshing: make(map[string]bool),
		sortCol:       SortColUsed,
		sortAsc:       false,
	}

	// Seed visible indexes with all sessions, unsorted for now.
	a.rebuildVisible()
	a.sortSessions()

	// Build widgets
	a.tabs = NewTabBar([]string{"Sessions", "Settings"})
	a.tabs.OnActivate = func(idx int) { a.activeTab = idx }
	a.table = NewTableWidget([]string{"NAME", "BASEDIR", "MODEL", "MSGS", "SUMMARY", "LAST USED", "CREATED"})
	a.table.SortCol = int(a.sortCol)
	a.table.SortAsc = a.sortAsc
	a.table.OnActivate = func(row int) { a.openSessionOptions(row) }
	a.table.OnSelect = func(row int) {
		a.trackSelection(row)
	}
	a.details = NewDetailsView()
	a.details.FormatTokens = a.formatSessionTokens
	a.status = &StatusBarWidget{Mode: StatusBrowse}

	a.populateTable()

	// If a ReturnTo session is provided, pre-select its row and set the
	// banner. The row is located after any sorting or filtering so that
	// the activation highlights the correct index.
	if opt.ReturnTo != nil {
		for vi, idx := range a.visibleIdx {
			if a.sessions[idx].Name == opt.ReturnTo.Name {
				a.table.Active = true
				a.table.SelectedRow = vi
				a.table.Offset = vi
				a.returnBanner = opt.ReturnTo.Name
				break
			}
		}
		a.openReturnPrompt(opt.ReturnTo)
	}
	return a
}

// openReturnPrompt shows the post-session modal with session stats and
// three choices: Return to session, Go back to session list, Quit clotilde.
// Quit is highlighted by default so a single Enter press exits.
func (a *App) openReturnPrompt(sess *session.Session) {
	prompt := &ReturnPrompt{
		SessionName: sess.Name,
		Stats:       a.buildReturnPromptStats(sess),
		Index:       3, // Quit is the default highlighted option
	}
	prompt.OnResume = func() {
		a.overlay = nil
		a.returnBanner = sess.Name
		if row := a.table.SelectedRow; row >= 0 && row < len(a.visibleIdx) {
			a.resumeRow(row)
		}
	}
	prompt.OnCompact = func() {
		a.overlay = nil
		a.openRichCompactForm(sess)
	}
	prompt.OnList = func() {
		a.overlay = nil
		a.returnBanner = sess.Name
	}
	prompt.OnQuit = func() {
		a.overlay = nil
		a.running = false
	}
	prompt.OnCancel = func() {
		a.overlay = nil
		a.returnBanner = sess.Name
	}
	a.overlay = prompt
}

// buildReturnPromptStats gathers the stat rows shown at the top of the
// post-session modal. Values come from the session metadata and the quick
// stats cache. Missing values are rendered as em-dash placeholders so the
// modal layout stays stable.
func (a *App) buildReturnPromptStats(sess *session.Session) []ReturnPromptStat {
	dash := "- -"
	stats := []ReturnPromptStat{
		{Label: "Model", Value: valueOr(a.modelCache[sess.Name], dash)},
		{Label: "Basedir", Value: shortPath(sess.Metadata.WorkspaceRoot)},
	}
	if qs, ok := a.statsCache[sess.Metadata.TranscriptPath]; ok && qs != nil {
		stats = append(stats,
			ReturnPromptStat{Label: "Tokens", Value: a.formatSessionTokens(sess, qs.EstimatedTokens)},
			ReturnPromptStat{Label: "Messages", Value: fmtInt(qs.TotalEntries)},
			ReturnPromptStat{Label: "Compactions", Value: fmtInt(qs.Compactions)},
		)
	} else {
		stats = append(stats,
			ReturnPromptStat{Label: "Tokens", Value: dash},
			ReturnPromptStat{Label: "Messages", Value: dash},
		)
	}
	stats = append(stats,
		ReturnPromptStat{Label: "Created", Value: sess.Metadata.Created.Format("2006-01-02 15:04")},
		ReturnPromptStat{Label: "Last used", Value: util.FormatRelativeTime(lastUsedTime(sess))},
	)
	return stats
}

// valueOr returns v if it is non-empty and not a placeholder, else fallback.
func valueOr(v, fallback string) string {
	if v == "" || v == "-" {
		return fallback
	}
	return v
}

// Run starts the event loop.
func (a *App) Run() error {
	if err := a.initScreen(); err != nil {
		return err
	}
	// Defer a sequenced teardown that always disables the alt-screen
	// modes we turned on. macOS Terminal and iTerm both leave the
	// cursor and mouse-tracking state half-set when only Fini runs.
	// The recover catches panics so a crash does not leave the user
	// stuck in alt-screen with mouse mode active.
	defer func() {
		if r := recover(); r != nil {
			a.teardownScreen()
			panic(r)
		}
		a.teardownScreen()
	}()

	a.running = true
	a.draw()

	// Ticker that posts a spinner tick every 100ms. The handler only
	// triggers a redraw when something is actually loading, so an idle
	// dashboard does not waste CPU.
	stopTicker := make(chan struct{})
	go a.runSpinnerTicker(stopTicker)
	defer close(stopTicker)

	// Watcher that polls the session store for changes every few seconds
	// and signals the main loop when something changed. Skipped while
	// the user is actively interacting.
	stopWatcher := make(chan struct{})
	go a.runStoreWatcher(stopWatcher)
	defer close(stopWatcher)

	// Subscribe to daemon registry events so adoptions land
	// immediately. Failure is tolerated: the polling watcher above
	// still keeps the dashboard reasonably fresh.
	if a.cb.SubscribeRegistry != nil {
		if events, cancel, err := a.cb.SubscribeRegistry(); err == nil {
			defer cancel()
			go a.runRegistrySubscriber(events)
		}
	}

	// Idle sweeper that regenerates stale session summaries one at a
	// time while the user is inactive. Rate limited so it never floods
	// the daemon or the upstream LLM.
	stopSweep := make(chan struct{})
	go a.runIdleSummarySweeper(stopSweep)
	defer close(stopSweep)

	for a.running {
		ev := a.screen.PollEvent()
		if ev == nil {
			return nil
		}
		a.handleEvent(ev)
		if a.running {
			a.draw()
		}
	}
	return nil
}

// spinnerTick is posted periodically while something is loading so the
// UI can advance the spinner glyph.
type spinnerTick struct{}

// runSpinnerTicker posts a spinnerTick every 100ms until stop is closed.
// The tick is cheap; handleEvent only repaints when data is actually
// pending so this does not burn CPU on an idle dashboard.
func (a *App) runSpinnerTicker(stop <-chan struct{}) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if a.screen == nil {
				continue
			}
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(spinnerTick{}))
		}
	}
}

// storeChanged is posted by the session watcher when the on-disk store
// contents differ from the last snapshot. The main loop calls
// softRefreshSessions in response, which preserves selection.
type storeChanged struct{}

// runStoreWatcher polls the session store every five seconds for changes.
// A fingerprint of (name, metadata mtime) pairs acts as a change signal
// so renames, deletes, and context updates all trigger a refresh. When a
// change is seen and the user has been idle for at least two seconds, a
// storeChanged interrupt is posted. Otherwise the refresh waits for the
// next tick. This keeps the dashboard fresh without thrashing the UI
// mid-scroll.
func (a *App) runStoreWatcher(stop <-chan struct{}) {
	if a.cb.Store == nil {
		return
	}
	const pollEvery = 5 * time.Second
	const idleGrace = 2 * time.Second
	a.storeSnapshot = a.storeFingerprint()
	t := time.NewTicker(pollEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if a.screen == nil {
				continue
			}
			fp := a.storeFingerprint()
			if fp == a.storeSnapshot {
				continue
			}
			// Check user activity. If a key or mouse event landed
			// recently, defer to the next tick so the refresh does
			// not reset a half-finished scroll or sort.
			a.interactionMu.Lock()
			lastAct := a.lastInteraction
			a.interactionMu.Unlock()
			if !lastAct.IsZero() && time.Since(lastAct) < idleGrace {
				continue
			}
			a.storeSnapshot = fp
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(storeChanged{}))
		}
	}
}

// storeFingerprint returns a short string that summarizes the current
// session store state. It is cheap to compute and changes whenever any
// session is added, removed, or has its metadata file modified.
func (a *App) storeFingerprint() string {
	if a.cb.Store == nil {
		return ""
	}
	sessions, err := a.cb.Store.List()
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, s := range sessions {
		fmt.Fprintf(&b, "%s|%d|", s.Name, s.Metadata.LastAccessed.UnixNano())
	}
	return b.String()
}

// noteInteraction records the current time as the last user interaction.
// Called from handleKey and handleMouse so the watcher can see activity.
func (a *App) noteInteraction() {
	a.interactionMu.Lock()
	a.lastInteraction = time.Now()
	a.interactionMu.Unlock()
}

// runRegistrySubscriber drains the daemon event stream and pokes the
// store watcher's interrupt path on every event. The reload runs on
// the same code path as the polling watcher so concurrency stays
// simple: the event loop owns all state mutations.
func (a *App) runRegistrySubscriber(events <-chan RegistryEvent) {
	for ev := range events {
		_ = ev
		if a.screen != nil {
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(a))
		}
	}
}

// runIdleSummarySweeper regenerates stale or missing session summaries
// while the user is idle. It wakes periodically, checks for a long idle
// window, picks one session whose Context looks outdated, and kicks off
// the same RefreshSummary path used on highlight. The rate is one
// candidate per sweep tick so the LLM workload stays low.
func (a *App) runIdleSummarySweeper(stop <-chan struct{}) {
	if a.cb.RefreshSummary == nil {
		return
	}
	const tick = 15 * time.Second
	const idleFor = 30 * time.Second
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if a.screen == nil {
				continue
			}
			a.interactionMu.Lock()
			lastAct := a.lastInteraction
			a.interactionMu.Unlock()
			if !lastAct.IsZero() && time.Since(lastAct) < idleFor {
				continue
			}
			sess := a.pickStaleForSweep()
			if sess == nil {
				continue
			}
			a.maybeRefreshSummary(sess)
		}
	}
}

// pickStaleForSweep returns the first session whose Context looks stale
// and whose refresh is not already in flight. Sessions without a
// transcript, incognito sessions, and ephemeral test sessions are
// skipped. Called only from the idle sweeper goroutine; the App fields
// it touches are either read-mostly or guarded.
func (a *App) pickStaleForSweep() *session.Session {
	for _, s := range a.sessions {
		if s == nil || s.Metadata.IsIncognito {
			continue
		}
		if s.Metadata.TranscriptPath == "" {
			continue
		}
		if isEphemeralSession(s) {
			continue
		}
		if a.summaryRefreshing[s.Name] {
			continue
		}
		ctx := strings.TrimSpace(s.Metadata.Context)
		words := 0
		if ctx != "" {
			words = len(strings.Fields(ctx))
		}
		stale := ctx == "" || words > 6
		if !stale {
			// Also consider stale when the transcript has many more
			// messages than the count stamped at last generation.
			if qs, ok := a.statsCache[s.Metadata.TranscriptPath]; ok && qs != nil {
				if qs.TotalEntries-s.Metadata.ContextMessageCount >= 20 {
					stale = true
				}
			}
		}
		if stale {
			return s
		}
	}
	return nil
}

// detailsLoadingNow reports whether the named session's details are being
// fetched in a goroutine. Used to gate spinner repaints.
func (a *App) detailsLoadingNow(name string) bool {
	a.detailMu.Lock()
	defer a.detailMu.Unlock()
	return a.detailLoading[name]
}

// syncTableSelectionWithOffset moves the selected row to stay in the visible
// window after a click-to-jump or drag on the table scrollbar. Without this
// the highlight would vanish off screen as the viewport scrolled.
func (a *App) syncTableSelectionWithOffset() {
	if !a.table.Active {
		return
	}
	vis := imax(1, a.table.Rect.H-1)
	if a.table.SelectedRow < a.table.Offset {
		a.table.SelectedRow = a.table.Offset
	} else if a.table.SelectedRow >= a.table.Offset+vis {
		a.table.SelectedRow = a.table.Offset + vis - 1
	}
}

// teardownScreen returns the terminal to a sensible state. Mouse and
// focus tracking are explicitly disabled before Fini so the host
// terminal does not keep emitting tracking sequences after exit.
// Calling this twice is safe.
func (a *App) teardownScreen() {
	if a.screen == nil {
		return
	}
	a.screen.DisableMouse()
	a.screen.DisableFocus()
	a.screen.ShowCursor(0, 0)
	a.screen.Fini()
	a.screen = nil
	// Some terminals (notably macOS Terminal.app and iTerm2 with
	// certain profiles) need a final reset to clear the alt-screen,
	// scroll-region, and mouse tracking state that tcell sometimes
	// leaves on. The escape sequence below is the standard "reset
	// everything" sequence: exit alt-screen, disable mouse modes,
	// clear scroll region, restore cursor.
	fmt.Fprint(os.Stdout, "\x1b[?1049l\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?25h\x1b[r")
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
	scr.EnableMouse(tcell.MouseButtonEvents | tcell.MouseDragEvents)
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
		a.refreshExactTokenCounts()
	}()
}

// refreshExactTokenCounts asks the Claude API for an authoritative token
// count per session. Runs only when ANTHROPIC_API_KEY is set so users
// without a key keep the local tiktoken estimate. Results land in
// exactTokenCache and the next redraw drops the "~" prefix for sessions
// the API was able to count.
func (a *App) refreshExactTokenCounts() {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return
	}
	go func() {
		for _, sess := range a.sessions {
			path := sess.Metadata.TranscriptPath
			if path == "" {
				continue
			}
			text := readTranscriptText(path)
			if text == "" {
				continue
			}
			n, err := transcript.CountTokensForText(apiKey, text)
			if err != nil || n <= 0 {
				continue
			}
			a.exactTokenMu.Lock()
			a.exactTokenCache[path] = n
			a.exactTokenMu.Unlock()
			a.asyncRefresh()
		}
	}()
}

// readTranscriptText returns a flattened text body of the transcript
// suitable for token counting. Only user and assistant text content
// is included. Tool blocks and system entries are skipped because they
// inflate token counts in ways that diverge from the in-context budget
// the dashboard cares about. Errors return an empty string so the
// caller skips this session quietly.
func readTranscriptText(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
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
		// Interrupts are posted from background goroutines. The Data
		// payload tells us which cache to refresh.
		switch d := e.Data().(type) {
		case detailsReady:
			// Only re-render the details pane if the user hasn't moved on.
			if a.selected != nil && a.selected.Name == d.name {
				a.populateDetails()
			}
		case spinnerTick:
			a.spinnerFrame++
			if a.selected != nil && a.detailsLoadingNow(a.selected.Name) {
				a.populateDetails()
			}
		case summaryRefreshed:
			// Table was already repopulated by maybeRefreshSummary. The
			// interrupt exists to trigger a draw cycle from the event loop.
			_ = d
		case storeChanged:
			a.softRefreshSessions()
		default:
			// Legacy callers post a raw *App; treat as a generic refresh.
			a.populateTable()
		}
	case *tcell.EventKey:
		a.handleKey(e)
	case *tcell.EventMouse:
		a.handleMouse(e)
	}
}

// handleKey dispatches keyboard events.
// Global shortcuts (Ctrl+C) always apply. Overlays take priority over widgets.
func (a *App) handleKey(e *tcell.EventKey) {
	a.noteInteraction()
	// Global: Ctrl+C always quits, regardless of focus.
	if e.Key() == tcell.KeyCtrlC {
		a.running = false
		return
	}

	if a.overlay != nil {
		a.overlay.HandleEvent(e)
		return
	}

	// When a details sub-pane is focused, scroll keys go to that pane.
	// Escape/Tab are handled globally below; action keys (r/v/s/d/c/etc.)
	// still work from details focus to avoid mode confusion.
	if a.detailsHasFocus() {
		switch e.Key() {
		case tcell.KeyUp, tcell.KeyDown, tcell.KeyPgUp, tcell.KeyPgDn,
			tcell.KeyHome, tcell.KeyEnd:
			a.details.HandleEvent(e)
			return
		case tcell.KeyRune:
			if r := e.Rune(); r == 'j' || r == 'k' || r == 'g' || r == 'G' {
				a.details.HandleEvent(e)
				return
			}
		}
	}

	// Mode-specific shortcuts that must fire before the table consumes keys.
	switch e.Key() {
	case tcell.KeyTab, tcell.KeyBacktab:
		// When the details pane is open, Tab cycles focus:
		//   table -> details.left -> details.right -> table
		// BackTab goes the other way.
		if a.selected != nil {
			a.cycleDetailsFocus(e.Key() == tcell.KeyBacktab)
			return
		}
	case tcell.KeyEscape:
		if a.detailsHasFocus() {
			a.details.SetFocus(DetailsFocusNone)
			return
		}
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
			// nvim-style: when a session is highlighted, "/" searches
			// inside that session's transcript. With no selection it
			// opens the table filter so the same key is always "find".
			if sess := a.rowSession(); sess != nil {
				a.openSearchForm()
				return
			}
			a.openFilter()
			return
		case '1':
			a.activeTab = 0
			a.tabs.SetActive(0)
			return
		case '2':
			a.activeTab = 1
			a.tabs.SetActive(1)
			return
		case '!':
			a.toggleSort(SortColName)
			return
		case '@':
			a.toggleSort(SortColWorkspace)
			return
		case '#':
			a.toggleSort(SortColModel)
			return
		case '$':
			a.toggleSort(SortColUsed)
			return
		case '%':
			a.toggleSort(SortColCreated)
			return
		// App-level shortcuts. We avoid binding lowercase letters that
		// the table uses for nvim-style movement (h/j/k/l/g/G) so the
		// movement keys fall through to the table widget below.
		case 'N':
			a.newSession()
			return
		case 'R':
			a.refreshSessions()
			return
		case 'B':
			if sess := a.rowSession(); sess != nil {
				a.openBasedirEditor(sess)
			}
			return
		case 'H':
			a.showEphemeral = !a.showEphemeral
			a.rebuildVisible()
			a.populateTable()
			return
		case 'O':
			if sess := a.rowSession(); sess != nil {
				a.openSessionOptionsFor(sess)
			}
			return
		case 'e':
			if a.activeTab == 1 {
				a.editConfigFile(false)
				return
			}
		case 'E':
			if a.activeTab == 1 {
				a.editConfigFile(true)
				return
			}
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
		case 'c':
			if a.selected != nil {
				a.openCompactForm()
			}
			return
		case '?':
			// Help screen with the full keymap. Uses the options modal
			// styling so it looks consistent with other overlays.
			a.openHelpModal()
			return
		}
	}

	// Fall through: table handles navigation.
	a.table.HandleEvent(e)
}

// handleMouse dispatches mouse events via direct rect hit tests.
// Overlays take priority. No InRect chain.
func (a *App) handleMouse(e *tcell.EventMouse) {
	// Only count buttoned events as interaction. Bare motion (no button)
	// should not block the watcher because macOS delivers a lot of idle
	// mouse-move events over the terminal window.
	if e.Buttons() != 0 {
		a.noteInteraction()
	}
	x, y := e.Position()
	btns := e.Buttons()

	if a.overlay != nil {
		a.overlay.HandleEvent(e)
		return
	}

	// Tab strip click takes priority over the rest of the body.
	if a.tabs != nil && a.tabs.HandleEvent(e) {
		return
	}

	// Release clears any active scrollbar grab.
	if btns == 0 {
		a.grab = grabNone
	}

	// If the user is currently dragging a scrollbar, keep routing the
	// mouse position to that widget until the button is released.
	if a.grab != grabNone && btns&tcell.Button1 != 0 {
		switch a.grab {
		case grabTable:
			a.table.JumpToScrollbarY(y)
			a.syncTableSelectionWithOffset()
		case grabDetailsLeft:
			if a.details != nil {
				a.details.Left.JumpToScrollbarY(y)
			}
		case grabDetailsRight:
			if a.details != nil {
				a.details.Right.JumpToScrollbarY(y)
			}
		}
		return
	}

	// Click on the table scrollbar starts a grab and jumps.
	if btns&tcell.Button1 != 0 && a.table.ScrollbarRect.Contains(x, y) {
		a.grab = grabTable
		a.table.JumpToScrollbarY(y)
		a.syncTableSelectionWithOffset()
		return
	}

	// Detail panes consume wheel events when the cursor is over them.
	// Each sub-pane scrolls independently, so hit-test both rects.
	if a.selected != nil && a.details != nil {
		// Scrollbar click or drag start on either sub-pane.
		if btns&tcell.Button1 != 0 && a.details.Left.ScrollbarRect.Contains(x, y) {
			a.grab = grabDetailsLeft
			a.details.SetFocus(DetailsFocusLeft)
			a.details.Left.JumpToScrollbarY(y)
			return
		}
		if btns&tcell.Button1 != 0 && a.details.Right.ScrollbarRect.Contains(x, y) {
			a.grab = grabDetailsRight
			a.details.SetFocus(DetailsFocusRight)
			a.details.Right.JumpToScrollbarY(y)
			return
		}
		if a.details.LeftRect.Contains(x, y) {
			if btns&tcell.WheelUp != 0 {
				a.details.Left.Offset = imax(0, a.details.Left.Offset-3)
				return
			}
			if btns&tcell.WheelDown != 0 {
				a.details.Left.Offset += 3
				return
			}
			if btns&tcell.Button1 != 0 {
				a.details.SetFocus(DetailsFocusLeft)
				return
			}
		}
		if a.details.RightRect.Contains(x, y) {
			if btns&tcell.WheelUp != 0 {
				a.details.Right.Offset = imax(0, a.details.Right.Offset-3)
				return
			}
			if btns&tcell.WheelDown != 0 {
				a.details.Right.Offset += 3
				return
			}
			if btns&tcell.Button1 != 0 {
				a.details.SetFocus(DetailsFocusRight)
				return
			}
		}
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
		if btns&tcell.WheelLeft != 0 {
			a.table.ScrollLeft(6)
			return
		}
		if btns&tcell.WheelRight != 0 {
			a.table.ScrollRight(6)
			return
		}
		// Left click / double click
		if btns&tcell.Button1 != 0 {
			// Header click sorts. The column index matches the display
			// order (NAME, BASEDIR, MODEL, MSGS, SUMMARY, LAST USED,
			// CREATED). MSGS and SUMMARY do not have their own sort
			// mode yet so clicks on those columns fall through.
			if y == a.tableRect.Y {
				// Table column order: NAME, BASEDIR, MODEL, MSGS,
				// SUMMARY, LAST USED, CREATED. Each clickable column
				// toggles its sort and flips the asc flag on a repeat
				// click of the same column.
				switch a.table.ColAtX(x) {
				case 0:
					a.toggleSort(SortColName)
				case 1:
					a.toggleSort(SortColWorkspace)
				case 2:
					a.toggleSort(SortColModel)
				case 3:
					a.toggleSort(SortColMessages)
				case 4:
					a.toggleSort(SortColSummary)
				case 5:
					a.toggleSort(SortColUsed)
				case 6:
					a.toggleSort(SortColCreated)
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

	// Clear the whole screen to the default style before redrawing. Each
	// widget also clears its own rect, but the margins between rects (the
	// two-column left and right gutters around the table, the row between
	// the top of the details pane and the table, and so on) are no
	// widget's responsibility. Without this wipe, stale runes from a
	// previous frame linger in those gutters and the UI visibly corrupts
	// the longer the user interacts with it.
	w, h := a.screen.Size()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a.screen.SetContent(x, y, ' ', nil, tcell.StyleDefault)
		}
	}

	// Tab strip (purple). Always visible across tabs so the user can
	// click between Sessions and Settings. The dashboard summary
	// renders right-aligned on the same row to save vertical space.
	a.tabs.Active = a.activeTab
	a.tabs.Draw(a.screen, Rect{X: 0, Y: 0, W: w, H: 1})

	right := fmt.Sprintf("clotilde  %d sessions", len(a.visibleIdx))
	if a.hiddenCount > 0 {
		right += fmt.Sprintf("  (%d hidden, H to show)", a.hiddenCount)
	} else if a.showEphemeral {
		right += "  (showing test/tmp)"
	}
	if a.filter != "" {
		right += fmt.Sprintf("  (filter: %q)", a.filter)
	}
	rx := w - runeCount(right) - 2
	if rx > 0 {
		drawString(a.screen, rx, 0, StyleTabBar.Bold(true), right, w-rx)
	}

	switch a.activeTab {
	case 0:
		// Table
		a.table.Draw(a.screen, a.tableRect)

		// Details
		if a.selected != nil {
			a.details.Draw(a.screen, a.detailRect)
		}
	default:
		a.drawSettingsTab(a.tableRect)
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
	summary := shortSummary(sess.Metadata.Context, 32)
	summaryStyle := StyleSubtext
	if summary == "" {
		summary = compactStyleEmpty
		summaryStyle = StyleDefault.Foreground(ColorMuted).Dim(true)
	}
	if isEphemeralSession(sess) {
		summaryStyle = StyleDefault.Foreground(ColorMuted).Dim(true)
	}
	msgs := ""
	if qs, ok := a.statsCache[sess.Metadata.TranscriptPath]; ok && qs != nil {
		msgs = fmtInt(qs.TotalEntries)
	}
	msgStyle := subStyle
	if msgs == "" {
		msgs = "-"
		msgStyle = StyleDefault.Foreground(ColorMuted).Dim(true)
	}
	return []TableCell{
		{Text: sess.Name, Style: nameStyle},
		{Text: shortPath(sess.Metadata.WorkspaceRoot), Style: subStyle},
		{Text: model, Style: modelStyle},
		{Text: msgs, Style: msgStyle},
		{Text: summary, Style: summaryStyle},
		{Text: util.FormatRelativeTime(lastUsedTime(sess)), Style: subStyle},
		{Text: sess.Metadata.Created.Format("Jan 02"), Style: subStyle},
	}
}

// compactStyleEmpty is the placeholder shown in the SUMMARY column when
// a session has not yet had its context generated.
const compactStyleEmpty = "(no summary yet)"

// shortSummary truncates a multi-line free-form context string into one
// line bounded by maxRunes so it fits a table cell. It picks the first
// non-blank line to avoid leading blank or decoration lines.
func shortSummary(ctx string, maxRunes int) string {
	s := strings.TrimSpace(ctx)
	if s == "" {
		return ""
	}
	// Collapse newlines to spaces and squash runs of whitespace.
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	if maxRunes > 0 {
		if rs := []rune(s); len(rs) > maxRunes {
			s = string(rs[:maxRunes-1]) + "…"
		}
	}
	return s
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
		case SortColMessages:
			less = sessionMessageCount(a, x) < sessionMessageCount(a, y)
		case SortColSummary:
			less = strings.ToLower(x.Metadata.Context) < strings.ToLower(y.Metadata.Context)
		case SortColCreated:
			less = x.Metadata.Created.Before(y.Metadata.Created)
		case SortColUsed:
			less = lastUsedTime(x).Before(lastUsedTime(y))
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
	if a.table != nil {
		a.table.SortCol = sortColTableIndex(col)
		a.table.SortAsc = a.sortAsc
	}
}

// sortColTableIndex maps a SortColumn to the table column index used by
// the table widget for header indicators.
func sortColTableIndex(c SortColumn) int {
	switch c {
	case SortColName:
		return 0
	case SortColWorkspace:
		return 1
	case SortColModel:
		return 2
	case SortColMessages:
		return 3
	case SortColSummary:
		return 4
	case SortColUsed:
		return 5
	case SortColCreated:
		return 6
	}
	return -1
}

// currentSession returns the session at the currently selected table row.
func (a *App) currentSession() *session.Session {
	if len(a.visibleIdx) == 0 || a.table.SelectedRow < 0 || a.table.SelectedRow >= len(a.visibleIdx) {
		return nil
	}
	return a.sessions[a.visibleIdx[a.table.SelectedRow]]
}

func (a *App) trackSelection(row int) {
	if row < 0 || row >= len(a.visibleIdx) {
		return
	}
	sess := a.sessions[a.visibleIdx[row]]
	// Keep details in sync if currently shown. The first arrow key
	// press also auto-opens the pane so the user gets context without
	// hitting Space first.
	if a.selected != nil {
		a.selected = sess
		a.populateDetails()
	} else if a.table.Active {
		a.openDetails(sess)
	}
	// Kick off a background summary refresh if the cached Context is
	// stale. This runs whether or not the details pane is open, so the
	// SUMMARY column stays fresh as the user moves through the list.
	a.maybeRefreshSummary(sess)
}

// maybeRefreshSummary decides whether to request a new Context for sess and
// dispatches the request through the daemon. It guards against duplicates
// via summaryRefreshing, and only fires when the transcript has grown at
// least 5 messages beyond the count recorded when Context was last set.
// A session with no Context is always refreshed.
func (a *App) maybeRefreshSummary(sess *session.Session) {
	if sess == nil || a.cb.RefreshSummary == nil {
		return
	}
	if sess.Metadata.IsIncognito || sess.Metadata.TranscriptPath == "" {
		return
	}
	if a.summaryRefreshing[sess.Name] {
		return
	}

	// How many user+assistant messages are currently in the transcript?
	// Heuristic: use the EntriesInContext field from the cached quick stats
	// when available; fall back to 0 (forces a refresh when Context is empty).
	msgNow := 0
	if qs, ok := a.statsCache[sess.Metadata.TranscriptPath]; ok && qs != nil {
		msgNow = qs.TotalEntries
	}

	// Criteria for refresh:
	//   1. No Context yet.
	//   2. Transcript grew by >= 5 entries since last generation.
	//   3. Stored Context is visibly too long. Earlier revisions of the
	//      daemon prompt produced sentence-length summaries that no
	//      longer fit the five-word column contract; treat anything
	//      over six words as stale so a regeneration runs.
	needs := false
	ctx := strings.TrimSpace(sess.Metadata.Context)
	wordCount := 0
	if ctx != "" {
		wordCount = len(strings.Fields(ctx))
	}
	switch {
	case ctx == "":
		needs = true
	case wordCount > 6:
		needs = true
	case msgNow > 0 && msgNow-sess.Metadata.ContextMessageCount >= 5:
		needs = true
	}
	if !needs {
		return
	}

	a.summaryRefreshing[sess.Name] = true
	name := sess.Name
	_ = a.cb.RefreshSummary(sess, func(updated *session.Session) {
		a.summaryRefreshing[name] = false
		if updated == nil {
			return
		}
		// Splice the updated session in and re-render the table row.
		for i := range a.sessions {
			if a.sessions[i].Name == name {
				a.sessions[i] = updated
				break
			}
		}
		a.populateTable()
		if a.screen != nil {
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(summaryRefreshed{}))
		}
	})
}

// summaryRefreshed signals that a background summary update completed.
type summaryRefreshed struct{}

// ---------------- Selection / details ----------------

// detailsHasFocus reports whether the details pane owns keyboard focus.
func (a *App) detailsHasFocus() bool {
	return a.selected != nil && a.details != nil && a.details.Focus != DetailsFocusNone
}

// cycleDetailsFocus advances focus through the three regions of the details
// layout in order: table -> left pane -> right pane -> back to table.
// If back is true, the cycle runs in reverse.
func (a *App) cycleDetailsFocus(back bool) {
	if a.details == nil {
		return
	}
	cur := a.details.Focus
	var next DetailsFocus
	if back {
		switch cur {
		case DetailsFocusNone:
			next = DetailsFocusRight
		case DetailsFocusLeft:
			next = DetailsFocusNone
		case DetailsFocusRight:
			next = DetailsFocusLeft
		}
	} else {
		switch cur {
		case DetailsFocusNone:
			next = DetailsFocusLeft
		case DetailsFocusLeft:
			next = DetailsFocusRight
		case DetailsFocusRight:
			next = DetailsFocusNone
		}
	}
	a.details.SetFocus(next)
}

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
	if a.details != nil {
		a.details.SetFocus(DetailsFocusNone)
	}
}

// populateDetails renders the details pane for the currently selected
// session. If a full SessionDetail is already cached it paints synchronously.
// Otherwise it paints a lightweight "loading" placeholder and kicks off a
// background goroutine via loadDetailAsync. When the goroutine finishes, it
// posts a tcell.EventInterrupt so the main loop repaints with the result.
func (a *App) populateDetails() {
	if a.selected == nil {
		return
	}
	name := a.selected.Name

	a.detailMu.Lock()
	cached, ok := a.detailCache[name]
	a.detailMu.Unlock()

	if ok {
		a.details.Set(a.selected, cached, a.statsCache)
		return
	}

	// Paint a fast placeholder so the UI is never blocked on disk I/O.
	placeholder := SessionDetail{Model: a.modelCache[name]}
	a.details.Set(a.selected, placeholder, a.statsCache)
	a.details.Left.Title = " STATS   " + spinnerGlyph(a.spinnerFrame) + " loading "
	a.details.Right.Title = " MESSAGES   " + spinnerGlyph(a.spinnerFrame) + " loading "

	a.loadDetailAsync(a.selected)
}

// loadDetailAsync spawns a goroutine to call cb.ExtractDetail and stash the
// result in detailCache. Duplicate calls for the same session are coalesced
// via detailLoading. Completion posts an interrupt event to wake the loop.
func (a *App) loadDetailAsync(sess *session.Session) {
	if a.cb.ExtractDetail == nil {
		return
	}
	name := sess.Name

	a.detailMu.Lock()
	if a.detailLoading[name] {
		a.detailMu.Unlock()
		return
	}
	a.detailLoading[name] = true
	a.detailMu.Unlock()

	go func() {
		detail := a.cb.ExtractDetail(sess)
		a.detailMu.Lock()
		a.detailCache[name] = detail
		delete(a.detailLoading, name)
		a.detailMu.Unlock()
		if a.screen != nil {
			_ = a.screen.PostEvent(tcell.NewEventInterrupt(detailsReady{name: name}))
		}
	}()
}

// detailsReady signals that a background ExtractDetail call completed.
// The main loop checks the selected session name against this payload so
// that stale completions (user moved on) do not cause a flash repaint.
type detailsReady struct{ name string }

// spinnerGlyph returns a single rotating spinner character for frame n.
func spinnerGlyph(n int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	return frames[n%len(frames)]
}

// ---------------- Actions ----------------

func (a *App) resumeRow(row int) {
	if row < 0 || row >= len(a.visibleIdx) || a.cb.ResumeSession == nil {
		return
	}
	sess := a.sessions[a.visibleIdx[row]]
	a.returnBanner = "" // acted on; don't re-prompt after the shell round trip
	a.suspendAndRun(func() {
		_ = a.cb.ResumeSession(sess)
	})
	// After claude exits, refresh the row's metadata (LastAccessed,
	// Context, etc.) and pop the post-session ReturnPrompt so the user
	// has the same Resume / List / Quit choices they get from the CLI
	// resume path. Without this, repeated resume-from-dashboard cycles
	// silently drop the user back to the table with no indication.
	a.refreshSessions()
	if updated := a.findSessionByName(sess.Name); updated != nil {
		a.openReturnPrompt(updated)
	} else {
		a.openReturnPrompt(sess)
	}
	// Re-select the row (refreshSessions resets selection on deselect).
	for vi, idx := range a.visibleIdx {
		if a.sessions[idx].Name == sess.Name {
			a.table.Active = true
			a.table.SelectedRow = vi
			break
		}
	}
}

func (a *App) newSession() {
	a.openNewSessionPrompt()
}

// openNewSessionPrompt opens the file picker so the user can navigate
// to the directory the new session should be anchored under. Pressing
// "s" in the picker commits the highlighted directory; Enter steps in.
// The directory is then passed to StartSessionWithBasedir. Esc cancels.
func (a *App) openNewSessionPrompt() {
	cwd, _ := os.Getwd()
	picker := NewFilePickerOverlay("Pick basedir for new session", cwd)
	picker.OnCancel = func() { a.closeOverlay() }
	picker.OnSelect = func(path string) {
		a.closeOverlay()
		basedir := strings.TrimSpace(path)
		runner := func() {
			if a.cb.StartSessionWithBasedir != nil {
				_ = a.cb.StartSessionWithBasedir(basedir)
				return
			}
			if a.cb.StartSession != nil {
				_ = a.cb.StartSession()
			}
		}
		a.suspendAndRun(runner)
		a.refreshSessions()
	}
	a.overlay = picker
	a.mode = StatusFilter
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
	a.openRichCompactForm(a.selected)
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

// editConfigFile suspends the TUI, opens the chosen config in the
// user's $EDITOR (defaulting to vi), then resumes. The project flag
// picks the per-project config; otherwise the global one. The file is
// created with sensible defaults if it does not exist so the editor
// always has something to open.
func (a *App) editConfigFile(project bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	var path string
	if project {
		cwd, _ := os.Getwd()
		path = filepath.Join(cwd, ".claude", "clotilde", "config.json")
	} else {
		path = filepath.Join(home, ".config", "clotilde", "config.toml")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		seed := "# clotilde config\n"
		if filepath.Ext(path) == ".json" {
			seed = "{}\n"
		}
		_ = os.WriteFile(path, []byte(seed), 0o644)
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	a.suspendAndRun(func() {
		cmd := exec.Command(editor, path)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Run()
	})
}

// drawSettingsTab renders the Settings tab body. It surfaces the active
// config file paths, the resolved values, and the actions a user can
// take. The body is read-mostly: editing happens in an external editor
// invoked via the e shortcut so the dashboard does not have to ship a
// full form widget for every config field.
func (a *App) drawSettingsTab(r Rect) {
	if r.W <= 0 || r.H <= 0 {
		return
	}

	home, _ := os.UserHomeDir()
	globalCfg := filepath.Join(home, ".config", "clotilde", "config.toml")
	globalCfgJSON := filepath.Join(home, ".config", "clotilde", "config.json")
	cwd, _ := os.Getwd()
	projectCfg := filepath.Join(cwd, ".claude", "clotilde", "config.json")

	type row struct {
		label string
		value string
		style tcell.Style
	}
	rows := []row{
		{label: "Settings", style: StyleDefault.Foreground(ColorAccent).Bold(true)},
		{},
		{label: "Global config", value: configRowDescription(globalCfg, globalCfgJSON), style: StyleSubtext},
		{label: "Project config", value: configRowDescription(projectCfg), style: StyleSubtext},
		{label: "Daemon log", value: filepath.Join(home, ".local", "state", "clotilde", "clotilde.jsonl"), style: StyleSubtext},
		{label: "Sessions root", value: filepath.Join(home, ".local", "share", "clotilde", "sessions"), style: StyleSubtext},
		{},
		{label: "Actions", style: StyleDefault.Foreground(ColorAccent).Bold(true)},
		{label: "  e  edit global config in $EDITOR", style: StyleSubtext},
		{label: "  E  edit project config in $EDITOR", style: StyleSubtext},
		{label: "  R  reload config (handled by daemon watcher)", style: StyleSubtext},
		{label: "  1  back to Sessions", style: StyleSubtext},
		{},
		{label: "Tip", style: StyleDefault.Foreground(ColorAccent).Bold(true)},
		{label: "  The daemon watches ~/.claude/settings.json and syncs across", style: StyleSubtext},
		{label: "  active sessions. Edit the file from any editor; the dashboard", style: StyleSubtext},
		{label: "  picks up changes automatically.", style: StyleSubtext},
	}

	for i, ln := range rows {
		text := ln.label
		if ln.value != "" {
			text = fmt.Sprintf("%-16s %s", ln.label, ln.value)
		}
		style := ln.style
		if style == (tcell.Style{}) {
			style = StyleSubtext
		}
		if i >= r.H-1 {
			break
		}
		drawString(a.screen, r.X+2, r.Y+1+i, style, text, r.W-4)
	}
}

// configRowDescription returns a "<path> (status)" string where status
// is one of "exists" or "missing". Useful for surfacing the active
// config files in the Settings tab without scattering os.Stat calls
// across the draw code.
func configRowDescription(paths ...string) string {
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p + "  (exists)"
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0] + "  (missing)"
}

// openHelpModal shows the full keymap. Triggered by "?" anywhere in
// the dashboard. The modal uses the same widget as the per-session
// options popup, with disabled entries used for the static rows.
func (a *App) openHelpModal() {
	close := func() { a.closeOverlay() }
	rows := []OptionsModalEntry{
		{Label: "Movement (nvim-style)", Disabled: true},
		{Label: "  j / ↓        next row", Disabled: true},
		{Label: "  k / ↑        prev row", Disabled: true},
		{Label: "  h / ←        scroll left", Disabled: true},
		{Label: "  l / →        scroll right", Disabled: true},
		{Label: "  g            top", Disabled: true},
		{Label: "  G            bottom", Disabled: true},
		{Label: "  PgUp/PgDn    page", Disabled: true},
		{Label: "Actions", Disabled: true},
		{Label: "  Enter / O    options for highlighted row", Disabled: true},
		{Label: "  Space        toggle details pane", Disabled: true},
		{Label: "  /            search transcript (or filter list)", Disabled: true},
		{Label: "  v            view transcript", Disabled: true},
		{Label: "  s            search transcript content", Disabled: true},
		{Label: "  c            compact session", Disabled: true},
		{Label: "  d            delete session", Disabled: true},
		{Label: "  f            fork session", Disabled: true},
		{Label: "Globals (Shift)", Disabled: true},
		{Label: "  N            new session", Disabled: true},
		{Label: "  R            refresh from disk", Disabled: true},
		{Label: "  B            edit basedir", Disabled: true},
		{Label: "  H            show/hide test sessions", Disabled: true},
		{Label: "  1-5          sort by column", Disabled: true},
		{Label: "  q / Esc      quit / cancel", Disabled: true},
		{Label: "  ?            this help", Disabled: true},
	}
	modal := NewOptionsModal("Keyboard shortcuts", rows)
	modal.OnCancel = close
	a.overlay = modal
}

// findSessionByName returns the in-memory session matching name, or
// nil. Used after a refresh to pick up updated metadata.
func (a *App) findSessionByName(name string) *session.Session {
	for _, s := range a.sessions {
		if s != nil && s.Name == name {
			return s
		}
	}
	return nil
}

// pushOverlay layers w on top of the current overlay, preserving the
// underlying one in the stack. Pop returns the user to it. Used by
// flows like compact-then-show-result that should not lose the panel
// the user was working in.
func (a *App) pushOverlay(w Widget) {
	if a.overlay != nil {
		a.overlayStack = append(a.overlayStack, a.overlay)
	}
	a.overlay = w
}

// popOverlay restores the previous overlay, if any, and returns true.
// Returns false when the stack is empty so the caller knows to fall
// back to its normal close behavior.
func (a *App) popOverlay() bool {
	if len(a.overlayStack) == 0 {
		a.overlay = nil
		return false
	}
	last := len(a.overlayStack) - 1
	a.overlay = a.overlayStack[last]
	a.overlayStack = a.overlayStack[:last]
	return true
}

// rowSession returns the session under the table cursor regardless of
// whether the details pane is currently showing it. Returns nil when no
// row is highlighted.
func (a *App) rowSession() *session.Session {
	if a.selected != nil {
		return a.selected
	}
	if a.table.SelectedRow < 0 || a.table.SelectedRow >= len(a.visibleIdx) {
		return nil
	}
	return a.sessions[a.visibleIdx[a.table.SelectedRow]]
}

// openSessionOptions shows the per-session options popup for the row at
// the given visible index. Used for table OnActivate (Enter or
// double-click). A no-op when the row is out of range.
func (a *App) openSessionOptions(row int) {
	if row < 0 || row >= len(a.visibleIdx) {
		return
	}
	a.openSessionOptionsFor(a.sessions[a.visibleIdx[row]])
}

// openSessionOptionsFor builds the options menu for the given session
// and installs it as the active overlay. Resume is the default cursor
// position so a user who just wants the old behavior types Enter twice.
func (a *App) openSessionOptionsFor(sess *session.Session) {
	if sess == nil {
		return
	}
	close := func() { a.closeOverlay() }
	entries := []OptionsModalEntry{
		{
			Label: "Resume",
			Hint:  "load this session",
			Action: func() {
				close()
				if a.cb.ResumeSession != nil {
					a.suspendAndRun(func() { _ = a.cb.ResumeSession(sess) })
					a.refreshSessions()
				}
			},
		},
		{
			Label: "View transcript",
			Hint:  "v",
			Action: func() {
				close()
				a.viewSelected()
			},
			Disabled: a.cb.ViewContent == nil,
		},
		{
			Label: "Edit basedir",
			Hint:  "b",
			Action: func() {
				close()
				a.openBasedirEditor(sess)
			},
		},
		{
			Label: "Rename",
			Hint:  "edits the registry name",
			Action: func() {
				close()
				a.openRenamePrompt(sess)
			},
			Disabled: a.cb.RenameSession == nil,
		},
		{
			Label: "Compact",
			Hint:  "c",
			Action: func() {
				close()
				a.openRichCompactForm(sess)
			},
		},
		{
			Label: "Fork",
			Hint:  "f",
			Action: func() {
				close()
				a.doFork()
			},
			Disabled: a.cb.ForkSession == nil,
		},
		{
			Label: "Delete",
			Hint:  "d",
			Action: func() {
				close()
				a.openDeleteConfirm()
			},
			Disabled: a.cb.DeleteSession == nil,
		},
	}
	modal := NewOptionsModal(sess.Name, entries)
	modal.OnCancel = close
	a.overlay = modal
}

// openRenamePrompt asks for the new session name via an inline input
// and routes the rename through the daemon when the callback is set.
// The wired callback hides whether the actual rename happened locally
// or via gRPC; either way the dashboard refreshes from the store on
// completion.
func (a *App) openRenamePrompt(sess *session.Session) {
	if sess == nil {
		return
	}
	input := NewTextInput("New name: ")
	input.Text = sess.Name
	input.CursorX = runeCount(sess.Name)
	input.OnSubmit = func(s string) {
		a.closeOverlay()
		newName := strings.TrimSpace(s)
		if newName == "" || newName == sess.Name {
			return
		}
		// Mutate the session's Name field so the callback wired in
		// cmd/root.go can read both old and new values from the
		// session struct without us widening the callback signature.
		oldName := sess.Name
		sess.Name = newName
		if a.cb.RenameSession != nil {
			if _, err := a.cb.RenameSession(sess); err != nil {
				// Best-effort restore so a failed rename does not
				// leave the in-memory session pointing at a name
				// that does not exist on disk.
				sess.Name = oldName
			}
		}
		a.refreshSessions()
	}
	input.OnCancel = a.closeOverlay
	a.overlay = &InputOverlay{Input: input, Title: "Rename session"}
	a.mode = StatusFilter
}

// openBasedirEditor pops up an inline single-line input pre-filled with
// the session's current workspace root. Submitting writes the new value
// through the SetBasedir callback. An empty result clears the field.
func (a *App) openBasedirEditor(sess *session.Session) {
	if sess == nil {
		return
	}
	current := sess.Metadata.WorkspaceRoot
	input := NewTextInput("Basedir: ")
	input.Text = current
	input.CursorX = runeCount(current)
	input.OnSubmit = func(s string) {
		a.closeOverlay()
		newPath := strings.TrimSpace(s)
		if newPath == current {
			return
		}
		if a.cb.SetBasedir != nil {
			if err := a.cb.SetBasedir(sess, newPath); err == nil {
				a.refreshSessions()
			}
		}
	}
	input.OnCancel = a.closeOverlay
	a.overlay = &InputOverlay{Input: input, Title: "Edit basedir for " + sess.Name + " (empty clears)"}
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
	// Invalidate cached details. The transcripts they were built from may
	// have grown or changed during the just-finished suspend.
	a.detailMu.Lock()
	a.detailCache = make(map[string]SessionDetail)
	a.detailMu.Unlock()
	a.deselect()
}

// softRefreshSessions reloads from the store without discarding the current
// selection, scroll position, filter, or details cache. Called by the
// background watcher when it notices on-disk changes. The previously
// selected session is re-located by name so renames carry through.
func (a *App) softRefreshSessions() {
	if a.cb.Store == nil {
		return
	}
	sessions, err := a.cb.Store.List()
	if err != nil {
		return
	}
	// Remember what the user was looking at.
	var selectedName string
	if a.selected != nil {
		selectedName = a.selected.Name
	} else if a.table.Active && a.table.SelectedRow < len(a.visibleIdx) {
		selectedName = a.sessions[a.visibleIdx[a.table.SelectedRow]].Name
	}
	prevOffset := a.table.Offset

	a.sessions = sessions
	a.sortSessions()
	a.populateTable()

	// Restore selection if the same name still exists.
	if selectedName != "" {
		for vi, idx := range a.visibleIdx {
			if a.sessions[idx].Name == selectedName {
				a.table.SelectedRow = vi
				if a.selected != nil {
					a.selected = a.sessions[idx]
				}
				break
			}
		}
	}
	a.table.Offset = prevOffset
	if a.selected != nil {
		// Refresh the details pane in case Context changed.
		a.detailMu.Lock()
		delete(a.detailCache, selectedName)
		a.detailMu.Unlock()
		a.populateDetails()
	}
}

// suspendAndRun shuts down the screen, runs fn (which may launch claude),
// then re-initializes the screen and repaints. This replaces tview's
// Suspend. The teardown path mirrors the Run defer so suspend leaves
// the terminal in the same clean state as a clean exit.
func (a *App) suspendAndRun(fn func()) {
	if a.screen == nil {
		fn()
		return
	}
	a.teardownScreen()
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

// sessionMessageCount returns the cached transcript entry count for a
// session. Sessions with no cached stats sort last (count -1 maps to a
// very small number for ascending order, very large for descending).
func sessionMessageCount(a *App, sess *session.Session) int {
	if sess == nil {
		return -1
	}
	qs, ok := a.statsCache[sess.Metadata.TranscriptPath]
	if !ok || qs == nil {
		return -1
	}
	return qs.TotalEntries
}

// formatSessionTokens renders the token count for a session, choosing
// between an exact (no prefix) and an estimated ("~" prefix) display
// based on whether the background API refresher has produced a Claude
// API result for the session's transcript.
func (a *App) formatSessionTokens(sess *session.Session, estimate int) string {
	if sess == nil {
		return fmtTokenCount(estimate, false)
	}
	a.exactTokenMu.Lock()
	exactN, ok := a.exactTokenCache[sess.Metadata.TranscriptPath]
	a.exactTokenMu.Unlock()
	if ok {
		return fmtTokenCount(exactN, true)
	}
	return fmtTokenCount(estimate, false)
}

// lastUsedTime returns the best available "last activity" timestamp for a
// session. Transcript file mtime is preferred because it advances on every
// message Claude appends, which is what the user actually means by "last
// used". When the transcript is missing or unreadable the metadata's
// LastAccessed timestamp serves as a fallback.
func lastUsedTime(sess *session.Session) time.Time {
	if sess == nil {
		return time.Time{}
	}
	if p := sess.Metadata.TranscriptPath; p != "" {
		if fi, err := os.Stat(p); err == nil {
			t := fi.ModTime()
			if t.After(sess.Metadata.LastAccessed) {
				return t
			}
		}
	}
	return sess.Metadata.LastAccessed
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
