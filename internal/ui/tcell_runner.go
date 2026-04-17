// Shared helpers for standalone tcell screens invoked outside the main App.
//
// Subcommands like `clotilde compact <name>` and `clotilde resume` need a
// transient full-screen TUI. They call one of the Run* helpers in this file,
// which initializes a tcell.Screen, drives a simple draw/event loop, and
// tears down on exit. The caller supplies a Widget to drive.
package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// runOverlay initializes a fresh tcell screen, displays the supplied overlay,
// and loops until finished is set to true by the overlay. The screen is
// finalized on exit in all cases.
//
// The overlay's HandleEvent must call the provided "done" closure when the
// user completes or cancels; runOverlay then exits. Ctrl+C always quits.
func runOverlay(build func(done func()) Widget) error {
	scr, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("tcell NewScreen: %w", err)
	}
	if err := scr.Init(); err != nil {
		return fmt.Errorf("tcell Init: %w", err)
	}
	defer scr.Fini()
	scr.EnableMouse(tcell.MouseButtonEvents | tcell.MouseDragEvents)
	scr.EnableFocus()
	scr.Clear()

	done := false
	overlay := build(func() { done = true })

	for !done {
		w, h := scr.Size()
		scr.Clear()
		overlay.Draw(scr, Rect{X: 0, Y: 0, W: w, H: h})
		scr.Show()

		ev := scr.PollEvent()
		if ev == nil {
			return nil
		}
		if ek, ok := ev.(*tcell.EventKey); ok && ek.Key() == tcell.KeyCtrlC {
			return nil
		}
		if _, ok := ev.(*tcell.EventResize); ok {
			scr.Sync()
			continue
		}
		if ef, ok := ev.(*tcell.EventFocus); ok && ef.Focused {
			scr.Sync()
			continue
		}
		overlay.HandleEvent(ev)
	}
	return nil
}
