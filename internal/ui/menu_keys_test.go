package ui

import (
	"testing"

	"github.com/gdamore/tcell/v2"
)

// TestMenuKey_EnterAndLFBothActivate is the regression for the bug
// the user reported as "I have to tap Enter twice on the post-session
// pane." Both KeyEnter (CR) and KeyLF (LF) must trigger OnActivate;
// previously each widget switched only on KeyEnter and missed LF
// after a screen teardown/reinit.
func TestMenuKey_EnterAndLFBothActivate(t *testing.T) {
	for _, key := range []tcell.Key{tcell.KeyEnter, tcell.KeyLF} {
		fired := 0
		cursor := 2
		ok := HandleMenuKey(
			tcell.NewEventKey(key, 0, tcell.ModNone),
			&cursor, 4,
			MenuKeyOptions{OnActivate: func(int) { fired++ }},
		)
		if !ok || fired != 1 {
			t.Errorf("key=%v: handled=%v, fired=%d, want true, 1", key, ok, fired)
		}
	}
}

// TestMenuKey_EscapeRunsOnCancel covers the dismiss path. It is
// separate from activation so widgets can wire different effects
// to each.
func TestMenuKey_EscapeRunsOnCancel(t *testing.T) {
	cancels := 0
	cursor := 0
	HandleMenuKey(
		tcell.NewEventKey(tcell.KeyEscape, 0, tcell.ModNone),
		&cursor, 3,
		MenuKeyOptions{OnCancel: func() { cancels++ }},
	)
	if cancels != 1 {
		t.Errorf("Esc cancels = %d, want 1", cancels)
	}
}

// TestMenuKey_UpDownClampsAtBounds confirms cursor never escapes
// the [0, count-1] range. It protects callers from index errors.
func TestMenuKey_UpDownClampsAtBounds(t *testing.T) {
	cursor := 0
	HandleMenuKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone), &cursor, 3, MenuKeyOptions{})
	if cursor != 0 {
		t.Errorf("Up at top: cursor = %d, want 0 (clamp)", cursor)
	}
	cursor = 2
	HandleMenuKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone), &cursor, 3, MenuKeyOptions{})
	if cursor != 2 {
		t.Errorf("Down at bottom: cursor = %d, want 2 (clamp)", cursor)
	}
}

// TestMenuKey_JKOptedIn confirms j/k navigate ONLY when EnableJK
// is set. ReturnPrompt enables it; widgets that accept text input
// do not.
func TestMenuKey_JKOptedIn(t *testing.T) {
	cursor := 1
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), &cursor, 5, MenuKeyOptions{EnableJK: false})
	if cursor != 1 {
		t.Errorf("j without EnableJK should be ignored; cursor moved to %d", cursor)
	}
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone), &cursor, 5, MenuKeyOptions{EnableJK: true})
	if cursor != 2 {
		t.Errorf("j with EnableJK: cursor = %d, want 2", cursor)
	}
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, 'k', tcell.ModNone), &cursor, 5, MenuKeyOptions{EnableJK: true})
	if cursor != 1 {
		t.Errorf("k with EnableJK: cursor = %d, want 1", cursor)
	}
}

// TestMenuKey_DigitJumpOptedIn confirms 1..9 only fire when the
// caller opts in. OptionsModal uses it for quick-pick; other
// widgets keep digits free for text input.
func TestMenuKey_DigitJumpOptedIn(t *testing.T) {
	cursor := 0
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone), &cursor, 8, MenuKeyOptions{AcceptDigitJump: false})
	if cursor != 0 {
		t.Errorf("digit without AcceptDigitJump should be ignored")
	}
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, '3', tcell.ModNone), &cursor, 8, MenuKeyOptions{AcceptDigitJump: true})
	if cursor != 2 {
		t.Errorf("digit jump '3' to cursor = %d, want 2", cursor)
	}
	cursor = 0
	HandleMenuKey(tcell.NewEventKey(tcell.KeyRune, '9', tcell.ModNone), &cursor, 5, MenuKeyOptions{AcceptDigitJump: true})
	if cursor != 0 {
		t.Errorf("digit '9' against count=5 should be ignored; cursor moved to %d", cursor)
	}
}

// TestMenuKey_QuitOptIn confirms q only fires when OnQuit is wired.
func TestMenuKey_QuitOptIn(t *testing.T) {
	quits := 0
	cursor := 0
	if handled := HandleMenuKey(
		tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone),
		&cursor, 3,
		MenuKeyOptions{},
	); handled {
		t.Errorf("q without OnQuit should not be consumed")
	}
	HandleMenuKey(
		tcell.NewEventKey(tcell.KeyRune, 'q', tcell.ModNone),
		&cursor, 3,
		MenuKeyOptions{OnQuit: func() { quits++ }},
	)
	if quits != 1 {
		t.Errorf("OnQuit fired %d times, want 1", quits)
	}
}
