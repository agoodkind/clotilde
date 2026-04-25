package ui

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestCompactPanelSliderAdjustsTarget(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.maxTokens = 1000000
	panel.targetTokens = 200000
	panel.targetText = "200000"
	panel.focusGroup = 0

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyRight, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected slider key to be handled")
	}
	if panel.targetTokens >= 200000 {
		t.Fatalf("expected right arrow to shrink target, got %d", panel.targetTokens)
	}

	handled = panel.HandleEvent(tcell.NewEventKey(tcell.KeyLeft, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected slider key to be handled")
	}
	if panel.targetTokens <= 190000 {
		t.Fatalf("expected left arrow to grow target, got %d", panel.targetTokens)
	}
}

func TestCompactPanelTargetInputParsesDigits(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.maxTokens = 1000000
	panel.targetText = ""
	panel.focusGroup = 1

	_ = panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone))
	_ = panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModNone))
	_ = panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, '0', tcell.ModNone))

	if panel.targetTokens != 300 {
		t.Fatalf("expected target 300, got %d", panel.targetTokens)
	}
}

func TestCompactPanelPreviewShortcutCallsCallback(t *testing.T) {
	panel := NewCompactPanel("demo")
	called := false
	panel.OnPreview = func(req CompactRunRequest) {
		called = true
		if req.TargetTokens <= 0 {
			t.Fatalf("expected target tokens in request")
		}
	}

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, 'p', tcell.ModNone))
	if !handled {
		t.Fatalf("expected preview shortcut to be handled")
	}
	if !called {
		t.Fatalf("expected preview callback to run")
	}
}

func TestCompactPanelArrowFocusNavigation(t *testing.T) {
	panel := NewCompactPanel("demo")

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected down key to move focus")
	}
	if panel.focusGroup != 1 {
		t.Fatalf("expected focus group 1 after down, got %d", panel.focusGroup)
	}

	handled = panel.HandleEvent(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected up key to move focus")
	}
	if panel.focusGroup != 0 {
		t.Fatalf("expected focus group 0 after up, got %d", panel.focusGroup)
	}
}

func TestCompactPanelTabDoesNotChangeFocus(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 2

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyTab, 0, tcell.ModNone))
	if handled {
		t.Fatalf("expected tab key to be ignored")
	}
	if panel.focusGroup != 2 {
		t.Fatalf("expected focus group unchanged, got %d", panel.focusGroup)
	}
}

func TestCompactPanelApplyRequiresConfirmation(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 3
	panel.actionIdx = 1
	applied := false
	panel.OnApply = func(req CompactRunRequest) {
		applied = true
	}

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected enter to be handled on actions row")
	}
	if applied {
		t.Fatalf("expected first enter to arm confirmation, not apply")
	}
	if !panel.confirmApply {
		t.Fatalf("expected apply confirmation to be armed")
	}

	handled = panel.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected second enter to be handled")
	}
	if !applied {
		t.Fatalf("expected second enter to apply after confirmation")
	}
}

func TestCompactPanelApplyRequiresConfirmationWithSpace(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 3
	panel.actionIdx = 1
	applied := false
	panel.OnApply = func(req CompactRunRequest) {
		applied = true
	}

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if !handled {
		t.Fatalf("expected space to be handled on actions row")
	}
	if applied {
		t.Fatalf("expected first space to arm confirmation, not apply")
	}
	if !panel.confirmApply {
		t.Fatalf("expected apply confirmation to be armed")
	}

	handled = panel.HandleEvent(tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone))
	if !handled {
		t.Fatalf("expected second space to be handled")
	}
	if !applied {
		t.Fatalf("expected second space to apply after confirmation")
	}
}

func TestCompactPanelEnterOutsideActionsDoesNotApply(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 1
	applied := false
	panel.OnApply = func(req CompactRunRequest) {
		applied = true
	}

	_ = panel.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if applied {
		t.Fatalf("expected enter outside actions to not apply")
	}
}

func TestCompactPanelCheckboxEnterToggles(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 2
	panel.checkboxIdx = 0
	panel.thinking = true

	handled := panel.HandleEvent(tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone))
	if !handled {
		t.Fatalf("expected enter to toggle focused checkbox")
	}
	if panel.thinking {
		t.Fatalf("expected thinking checkbox to toggle off")
	}
}

func TestPercentLabelUsesCommaGrouping(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.maxTokens = 1000000
	panel.targetTokens = 200000

	label := panel.percentLabel()
	if label != "20% (200,000/1,000,000)" {
		t.Fatalf("unexpected percent label: %q", label)
	}
}

func TestCompactPanelSliderFillRepresentsCompactedShare(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.maxTokens = 1000000
	panel.targetTokens = 200000

	got := panel.renderSlider(20)
	want := "|================----|"
	if got != want {
		t.Fatalf("slider = %q want %q", got, want)
	}
}

func TestCompactPanelStatusLegendUsesEnumActions(t *testing.T) {
	panel := NewCompactPanel("demo")
	actions := panel.StatusLegendActions()
	if len(actions) == 0 {
		t.Fatalf("expected compact panel legend actions")
	}
	hasSelect := false
	for _, action := range actions {
		if action == LegendSelect {
			hasSelect = true
			break
		}
	}
	if !hasSelect {
		t.Fatalf("expected compact panel legend actions to include LegendSelect")
	}
	for _, action := range actions {
		if action == LegendPreview || action == LegendApply || action == LegendUndo {
			t.Fatalf("expected compact panel legend actions to stay succinct, got action %v", action)
		}
	}
}

func TestCompactPanelRenderActionsLooksLikeButtons(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.focusGroup = 3
	panel.actionIdx = 1
	rendered := panel.renderActions()
	if !strings.Contains(rendered, "[ Preview ]") {
		t.Fatalf("expected preview button rendering, got %q", rendered)
	}
	if !strings.Contains(rendered, "[> Apply <]") {
		t.Fatalf("expected focused apply button rendering, got %q", rendered)
	}
}

func TestCompactPanelVisibleIterationLinesShowsLatestSteps(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.iterationHistory = []CompactIteration{
		{Iteration: 1, Step: "baseline", CtxTotal: 975789, Delta: 475789},
		{Iteration: 2, Step: "drop thinking", CtxTotal: 975700, Delta: 475700},
		{Iteration: 3, Step: "drop tools", CtxTotal: 506400, Delta: 6400},
	}

	lines := panel.visibleIterationLines(2)
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if !strings.Contains(lines[0], "iter 2") || !strings.Contains(lines[0], "drop thinking") {
		t.Fatalf("unexpected first visible line: %q", lines[0])
	}
	if !strings.Contains(lines[1], "iter 3") || !strings.Contains(lines[1], "506,400") || !strings.Contains(lines[1], "+6,400") {
		t.Fatalf("unexpected second visible line: %q", lines[1])
	}
}

func TestCompactPanelApplyCompactEventTracksIterationHistory(t *testing.T) {
	panel := NewCompactPanel("demo")
	panel.ApplyCompactEvent(CompactEvent{
		Kind: "upfront",
		Upfront: &CompactUpfront{
			SessionName: "demo",
			SessionID:   "sess-1",
			Model:       "opus",
		},
	})
	panel.ApplyCompactEvent(CompactEvent{
		Kind: "iteration",
		Iteration: &CompactIteration{
			Iteration: 1,
			Step:      "baseline",
			CtxTotal:  900000,
			Delta:     400000,
		},
	})
	panel.ApplyCompactEvent(CompactEvent{
		Kind: "iteration",
		Iteration: &CompactIteration{
			Iteration: 2,
			Step:      "drop chat",
			CtxTotal:  500100,
			Delta:     100,
		},
	})

	if panel.latestIteration == nil || panel.latestIteration.Iteration != 2 {
		t.Fatalf("expected latest iteration 2, got %#v", panel.latestIteration)
	}
	if got := len(panel.iterationHistory); got != 2 {
		t.Fatalf("expected 2 history rows, got %d", got)
	}
}
