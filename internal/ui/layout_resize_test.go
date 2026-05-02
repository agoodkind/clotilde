package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"

	"goodkind.io/clyde/internal/session"
)

func TestLayoutKeepsRectsWithinScreen(t *testing.T) {
	a, scr, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()

	type sizeCase struct {
		w int
		h int
	}
	cases := []sizeCase{
		{w: 4, h: 4},
		{w: 8, h: 8},
		{w: 20, h: 8},
		{w: 48, h: 14},
		{w: 120, h: 30},
	}

	for _, tc := range cases {
		scr.SetSize(tc.w, tc.h)
		a.selected = nil
		a.layout()
		assertRectInBounds(t, "header", a.headerRect, tc.w, tc.h)
		assertRectInBounds(t, "table", a.tableRect, tc.w, tc.h)
		assertRectInBounds(t, "status", a.statusRect, tc.w, tc.h)
		if a.detailRect.H != 0 || a.detailRect.W != 0 {
			assertRectInBounds(t, "detail", a.detailRect, tc.w, tc.h)
		}

		// Re-run with details enabled and assert bounds again.
		if len(a.visibleIdx) > 0 {
			a.selected = a.sessions[a.visibleIdx[0]]
		}
		a.layout()
		assertRectInBounds(t, "header(selected)", a.headerRect, tc.w, tc.h)
		assertRectInBounds(t, "table(selected)", a.tableRect, tc.w, tc.h)
		assertRectInBounds(t, "status(selected)", a.statusRect, tc.w, tc.h)
		if a.detailRect.H != 0 || a.detailRect.W != 0 {
			assertRectInBounds(t, "detail(selected)", a.detailRect, tc.w, tc.h)
		}
	}
}

func TestDetailsViewResizeUpdatesHitRects(t *testing.T) {
	d := mkDetailsView()
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer scr.Fini()

	// Start wide: expect both panes.
	scr.SetSize(120, 30)
	d.Draw(scr, Rect{X: 0, Y: 0, W: 120, H: 30})
	if d.RightRect.W == 0 || d.RightRect.H == 0 {
		t.Fatalf("wide draw should populate right rect, got %+v", d.RightRect)
	}

	// Then tiny: expect only left pane and a cleared right hit rect.
	scr.SetSize(40, 20)
	d.Draw(scr, Rect{X: 0, Y: 0, W: 40, H: 20})
	if d.RightRect.W != 0 || d.RightRect.H != 0 {
		t.Fatalf("tiny draw should clear right rect, got %+v", d.RightRect)
	}
	if d.LeftRect.W == 0 || d.LeftRect.H == 0 {
		t.Fatalf("tiny draw should keep left rect usable, got %+v", d.LeftRect)
	}
}

func TestOptionsModalHandlesTinyScreensWithOptionsOnly(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(36, 12)

	activated := 0
	modal := NewOptionsModal("Session exited: tiny", []OptionsModalEntry{
		{
			Label: "Resume",
			Hint:  "r",
			Action: func() {
				activated++
			},
		},
	})
	modal.Context = OptionsModalContextReturn
	modal.TopEntries = []OptionsModalEntry{
		{
			Label: "Quit clyde",
			Hint:  "q",
			Action: func() {
				activated++
			},
		},
	}
	modal.StatsSegments = [][]TextSegment{
		{{Text: "STATS", Style: StyleDefault.Bold(true)}},
		{{Text: "Model: opus", Style: StyleSubtext}},
	}
	modal.resetCursor()
	modal.Draw(scr, Rect{X: 0, Y: 0, W: 36, H: 12})

	if len(modal.entryRects) < 2 {
		t.Fatalf("expected entry rects for top+base entries, got %d", len(modal.entryRects))
	}
	handled := modal.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected enter to be handled on tiny modal")
	}
	if activated != 1 {
		t.Fatalf("expected one action activation on tiny modal, got %d", activated)
	}
}

func TestLayoutResumeReturnPromptSurvivesResizeBurst(t *testing.T) {
	a, scr, cleanup := mkAppWithSessions(t, 3)
	defer cleanup()
	a.suspendImpl = func(fn func()) { fn() }
	a.cb.ResumeSession = func(*session.Session) error { return nil }

	a.table.SelectedRow = 0
	a.table.Active = true
	a.resumeRow(0)

	modal, ok := a.overlay.(*OptionsModal)
	if !ok || modal.Context != OptionsModalContextReturn {
		t.Fatalf("overlay = %T, want return-context *OptionsModal", a.overlay)
	}

	sizes := []struct {
		width  int
		height int
	}{
		{120, 30},
		{92, 26},
		{76, 22},
		{104, 28},
		{84, 24},
	}
	for _, size := range sizes {
		scr.SetSize(size.width, size.height)
		a.handleEvent(tcell.NewEventResize(size.width, size.height))
	}

	afterResize, ok := a.overlay.(*OptionsModal)
	if !ok || afterResize.Context != OptionsModalContextReturn {
		t.Fatalf("return prompt disappeared during resize burst: overlay=%T", a.overlay)
	}
	if afterResize.OnCancel == nil {
		t.Fatalf("return prompt missing cancel handler")
	}
	afterResize.OnCancel()
	if a.overlay != nil {
		t.Fatalf("overlay should close after cancel")
	}
}

func TestEventResizeSetsPendingDisplaySyncWithoutTableReshuffle(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 2)
	defer cleanup()
	before := len(a.table.Rows)
	a.handleEvent(tcell.NewEventResize(120, 30))
	if !a.pendingResizeDisplaySync {
		t.Fatalf("expected pendingResizeDisplaySync after EventResize")
	}
	if len(a.table.Rows) != before {
		t.Fatalf("EventResize must not repopulate the table, got row count %d want %d",
			len(a.table.Rows), before)
	}
}

func TestLastUsedTickDoesNotDeadlockWhenUpdatingRows(t *testing.T) {
	a, _, cleanup := mkAppWithSessions(t, 2)
	defer cleanup()

	transcriptPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcriptPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	a.sessions[0].Metadata.SetProviderTranscriptPath(transcriptPath)

	done := make(chan struct{})
	go func() {
		a.handleEvent(tcell.NewEventInterrupt(lastUsedTick{}))
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("lastUsedTick handling deadlocked")
	}
}

func assertRectInBounds(t *testing.T, name string, rect Rect, width int, height int) {
	t.Helper()
	if rect.W < 0 || rect.H < 0 {
		t.Fatalf("%s has negative size: %+v", name, rect)
	}
	if rect.X < 0 || rect.Y < 0 {
		t.Fatalf("%s has negative origin: %+v", name, rect)
	}
	if rect.X > width || rect.Y > height {
		t.Fatalf("%s origin is outside screen: rect=%+v screen=%dx%d", name, rect, width, height)
	}
	if rect.X+rect.W > width || rect.Y+rect.H > height {
		t.Fatalf("%s exceeds screen bounds: rect=%+v screen=%dx%d", name, rect, width, height)
	}
}
