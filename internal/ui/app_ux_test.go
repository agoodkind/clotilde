package ui

import (
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"goodkind.io/clyde/internal/session"
)

func TestUX_OpenReturnPromptDoesNotBlockOnDetailExtraction(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 2)
	defer cleanup()
	block := make(chan struct{})
	a.cb.ExtractDetail = func(*session.Session) SessionDetail {
		<-block
		return SessionDetail{Model: "opus"}
	}

	start := time.Now()
	sess := a.sessions[a.visibleIdx[0]]
	a.openReturnPrompt(sess)
	elapsed := time.Since(start)

	if elapsed > 50*time.Millisecond {
		t.Fatalf("openReturnPrompt blocked on detail extraction: %s", elapsed)
	}
	modal, ok := a.overlay.(*OptionsModal)
	if !ok || modal.Context != OptionsModalContextReturn {
		t.Fatalf("overlay = %T, want return-context *OptionsModal", a.overlay)
	}

	close(block)
}

// TestUX_FirstDownArmsFirstRow verifies first-launch keyboard behavior:
// the first Down key press should arm/highlight row 0, and only the
// second Down key press should move to row 1.
func TestUX_FirstDownArmsFirstRow(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 5)
	defer cleanup()

	// Initial state assertion: dashboard opens on row 0.
	if a.table.SelectedRow != 0 {
		t.Fatalf("initial SelectedRow = %d, want 0  --  first-launch should not skip", a.table.SelectedRow)
	}
	if !a.table.Active && len(a.visibleIdx) > 0 {
		// Active gets set by any navigation. Before first Down it
		// may be false; the highlight shouldn't show until the user
		// interacts. That's fine.
	}

	// ONE Down. First movement key should arm/highlight row 0.
	a.table.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if a.table.SelectedRow != 0 {
		t.Errorf("after 1×Down, SelectedRow = %d, want 0 (first row armed)", a.table.SelectedRow)
	}
	// Second Down  --  row 1.
	a.table.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if a.table.SelectedRow != 1 {
		t.Errorf("after 2×Down, SelectedRow = %d, want 1", a.table.SelectedRow)
	}
}

// TestUX_FirstEnterOpensOptions counts how many Enter presses it
// takes to open the options popup from the fresh-launch dashboard.
// Expected: exactly one.
func TestUX_FirstEnterOpensOptions(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.table.SelectedRow = 0
	a.table.Active = true

	// Simulate Enter via the table's OnActivate, which is what
	// handleEvent invokes when Enter hits the table widget.
	enters := 0
	for a.overlay == nil && enters < 3 {
		enters++
		a.table.OnActivate(a.table.SelectedRow)
	}
	if enters != 1 {
		t.Errorf("Enter-presses to open options popup = %d, want 1", enters)
	}
	if _, ok := a.overlay.(*OptionsModal); !ok {
		t.Errorf("overlay after Enter = %T, want *OptionsModal", a.overlay)
	}
}

// TestUX_PostSessionPromptPicksTheListOption mirrors the user's
// concrete flow: resume a session, claude exits, the post-session
// prompt shows, user picks "Go back to session list", expects the
// dashboard to be responsive and the prompt to be gone.
//
// The bug the user just hit this morning in post-session pane:
// unclear, but the most likely regressions are:
//   - Prompt doesn't appear after exit
//   - Picking "list" leaves the overlay stuck
//   - After list, keys don't route to the table
//
// This test covers all three.
func TestUX_PostSessionPromptPicksTheListOption(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 4)
	defer cleanup()
	a.suspendImpl = func(fn func()) { fn() }
	resumeCalls := 0
	a.cb.ResumeSession = func(*session.Session) error {
		resumeCalls++
		return nil
	}

	// Trigger resume on row 0.
	a.table.SelectedRow = 0
	a.table.Active = true
	a.resumeRow(0)

	if resumeCalls != 1 {
		t.Fatalf("ResumeSession calls = %d, want 1", resumeCalls)
	}
	modal, ok := a.overlay.(*OptionsModal)
	if !ok || modal.Context != OptionsModalContextReturn {
		t.Fatalf("overlay = %T, want return-context *OptionsModal (post-session pane missing)", a.overlay)
	}

	// User picks "Go back to session list".
	listAction := findModalAction(modal, "Go to session list")
	if listAction == nil {
		t.Fatalf("return modal missing Go to session list action")
	}
	listAction()
	if a.overlay != nil {
		t.Errorf("after OnList, overlay = %T, want nil (prompt should dismiss)", a.overlay)
	}

	// Now the dashboard should be responsive again. Table should
	// accept a Down event and move the selection.
	before := a.table.SelectedRow
	a.table.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if a.table.SelectedRow == before {
		t.Errorf("after Dismissing prompt, Down did not move selection  --  table frozen")
	}
}

// TestUX_ReturnPromptResumeAgainClosesLoop drives the full loop
// the user described: resume → exit → prompt → resume → exit →
// prompt → … N times. Each iteration should land the prompt open
// with the four entries visible. If any iteration comes back with
// a nil overlay or wrong type, the test fails with the cycle index
// so the regression is easy to pinpoint.
func TestUX_ReturnPromptResumeAgainClosesLoop(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.suspendImpl = func(fn func()) { fn() }
	a.cb.ResumeSession = func(*session.Session) error { return nil }

	a.table.SelectedRow = 0
	a.table.Active = true

	for i := 1; i <= 5; i++ {
		a.resumeRow(0)
		modal, ok := a.overlay.(*OptionsModal)
		if !ok || modal.Context != OptionsModalContextReturn {
			t.Fatalf("cycle %d: overlay = %T, want return-context *OptionsModal", i, a.overlay)
		}
		// Call List to reset the loop so the next resumeRow fires
		// from a clean slate (OnResume would recurse through another
		// resumeRow internally; testing that path is
		// TestHappyPath_ResumeCycleMultipleTimes).
		listAction := findModalAction(modal, "Go to session list")
		if listAction == nil {
			t.Fatalf("cycle %d: return modal missing list action", i)
		}
		listAction()
		if a.overlay != nil {
			t.Errorf("cycle %d: OnList did not clear overlay", i)
		}
	}
}

// TestUX_SearchSlashWorksOnFreshLaunch catches the bug the PTY
// suite just uncovered: pressing `/` on a freshly-launched dashboard
// silently did nothing because openSearchForm bailed when
// a.selected was nil. Now rowSession() is the source of truth.
func TestUX_SearchSlashWorksOnFreshLaunch(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.table.SelectedRow = 0
	a.table.Active = true
	// a.selected is deliberately NIL  --  this is the post-launch
	// state before the user has pressed Space.
	if a.selected != nil {
		t.Fatalf("test precondition: a.selected should be nil")
	}
	a.handleKey(tcell.NewEventKey(tcell.KeyRune, '/', tcell.ModNone))
	if a.overlay == nil {
		t.Errorf("after `/` on fresh launch, overlay is nil  --  silent no-op regression")
	}
}

// TestUX_EscapeOnBareDashboardDoesNotQuit confirms Esc on the
// dashboard (no overlay, no selection) is a safe no-op, not a quit.
// Earlier the user hit unexpected exits from stray Esc presses in
// various resumed states.
func TestUX_EscapeOnBareDashboardDoesNotQuit(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.running = true

	a.handleKey(tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone))
	if !a.running {
		t.Errorf("Esc on bare dashboard flipped a.running to false  --  unexpected quit")
	}
	if a.overlay != nil {
		t.Errorf("Esc on bare dashboard created an overlay: %T", a.overlay)
	}
}

// TestUX_QOnRowDeselectsThenQuits confirms the two-stage q behavior:
// first q with a session selected (details open) deselects, second
// q with nothing selected quits. The current production code checks
// a.selected != nil  --  so Space-then-q deselects, another q quits.
func TestUX_QOnRowDeselectsThenQuits(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.running = true
	// Open details on row 0 so a.selected is set.
	a.table.SelectedRow = 0
	a.openDetails(a.sessions[a.visibleIdx[0]])
	if a.selected == nil {
		t.Fatal("openDetails did not set a.selected")
	}

	// First q: should deselect (no quit).
	a.handleKey(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if !a.running {
		t.Errorf("first q with details open quit unexpectedly")
	}
	if a.selected != nil {
		t.Errorf("first q did not deselect (a.selected=%v)", a.selected)
	}

	// Second q: should flip running to false (quit signal).
	a.handleKey(tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone))
	if a.running {
		t.Errorf("second q with nothing selected did not quit")
	}
}

// TestUX_PostSessionPaneEnterAcceptsBothCRandLF is the regression
// test for the bug the user reported as "I have to tap Enter two
// or three times before quit registers."
//
// Root cause: the ReturnPrompt (and every other Enter handler in the
// UI) was checking only tcell.KeyEnter (CR / 0x0D). After the screen
// teardown / reinit cycle inside suspendAndRun some terminals emit
// LF (0x0A) for Enter, which decodes as tcell.KeyLF  --  a separate
// key tcell does not collapse into KeyEnter. The first one or two
// keypresses landed as KeyLF, the handler ignored them, and the
// next CR-emitting press finally triggered the activation. From the
// user's seat that looks like an unresponsive prompt.
//
// This test drives a synthetic KeyLF and asserts the prompt's
// activate path fires exactly once.
func TestUX_PostSessionPaneEnterAcceptsBothCRandLF(t *testing.T) {
	for _, key := range []tcell.Key{tcell.KeyEnter, tcell.KeyLF} {
		t.Run(keyName(key), func(t *testing.T) {
			a, _, cleanup := mkAppWithSessions(t, 3)
			defer cleanup()
			a.suspendImpl = func(fn func()) { fn() }
			a.cb.ResumeSession = func(*session.Session) error { return nil }

			a.table.SelectedRow = 0
			a.table.Active = true
			a.resumeRow(0)

			modal, ok := a.overlay.(*OptionsModal)
			if !ok || modal.Context != OptionsModalContextReturn {
				t.Fatalf("post-session pane missing: overlay = %T", a.overlay)
			}
			// Default highlight is Quit. One Enter (or LF) must trigger quit.
			a.running = true
			handled := modal.HandleEvent(tcell.NewEventKey(key, 0, tcell.ModNone))
			if !handled {
				t.Errorf("HandleEvent returned false for key %v  --  prompt did not consume the press", key)
			}
			if a.running {
				t.Errorf("a.running still true after quit activation")
			}
		})
	}
}

// keyName renders a tcell.Key for sub-test naming.
func keyName(k tcell.Key) string {
	switch k {
	case tcell.KeyEnter:
		return "KeyEnter-CR-0x0D"
	case tcell.KeyLF:
		return "KeyLF-0x0A"
	default:
		return "Key-other"
	}
}

func findModalAction(modal *OptionsModal, label string) func() {
	for _, entry := range modal.TopEntries {
		if entry.Label == label {
			return entry.Action
		}
	}
	for _, entry := range modal.Entries {
		if entry.Label == label {
			return entry.Action
		}
	}
	return nil
}

// TestUX_OpenDetailsSpaceRequiresOneTap counts Space presses to open
// the details pane from the freshly-launched dashboard.
func TestUX_OpenDetailsSpaceRequiresOneTap(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.table.SelectedRow = 0
	a.table.Active = true

	if a.selected != nil {
		t.Fatalf("a.selected should be nil pre-Space")
	}
	a.handleKey(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if a.selected == nil {
		t.Errorf("one Space did not open details pane")
	}
	// Counting proof: exactly one tap was enough.
}
