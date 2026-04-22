package ui

import (
	"fmt"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestOptionsModalSpaceAndEnterBothActivate(t *testing.T) {
	for _, key := range []tcell.Key{tcell.KeyEnter, tcell.KeyRune} {
		t.Run(fmt.Sprintf("key-%v", key), func(t *testing.T) {
			activated := 0
			modal := NewOptionsModal("x", []OptionsModalEntry{
				{
					Label: "Resume",
					Action: func() {
						activated++
					},
				},
			})
			var ev *tcell.EventKey
			if key == tcell.KeyEnter {
				ev = tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModNone)
			} else {
				ev = tcell.NewEventKey(tcell.KeyRune, ' ', tcell.ModNone)
			}
			handled := modal.HandleEvent(ev)
			if !handled {
				t.Fatalf("key %v was not handled", key)
			}
			if activated != 1 {
				t.Fatalf("key %v activated=%d want 1", key, activated)
			}
		})
	}
}

func TestOptionsModalIncludesGapBetweenTopAndSharedRows(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(90, 24)

	modal := NewOptionsModal("return", []OptionsModalEntry{
		{Label: "Resume", Action: func() {}},
	})
	modal.Context = OptionsModalContextReturn
	modal.TopEntries = []OptionsModalEntry{
		{Label: "Quit clyde", Action: func() {}},
	}
	modal.Draw(scr, Rect{X: 0, Y: 0, W: 90, H: 24})

	if modal.optionsTotalRows != 3 {
		t.Fatalf("optionsTotalRows=%d want 3 (top + gap + base)", modal.optionsTotalRows)
	}
}

func TestOptionsModalMouseWheelScrollsStatsPane(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(100, 20)

	segments := make([][]TextSegment, 0, 40)
	for i := 0; i < 40; i++ {
		segments = append(segments, []TextSegment{{Text: fmt.Sprintf("line %d", i), Style: StyleDefault}})
	}

	entries := make([]OptionsModalEntry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, OptionsModalEntry{
			Label:  fmt.Sprintf("Entry %d", i),
			Action: func() {},
		})
	}
	modal := NewOptionsModal("stats", entries)
	modal.StatsSegments = segments
	modal.Draw(scr, Rect{X: 0, Y: 0, W: 100, H: 20})

	if modal.statsRect.W == 0 || modal.statsRect.H == 0 {
		t.Fatalf("stats pane not drawn")
	}
	x := modal.statsRect.X + 1
	y := modal.statsRect.Y + 1
	handled := modal.HandleEvent(tcell.NewEventMouse(x, y, tcell.WheelDown, tcell.ModNone))
	if !handled {
		t.Fatalf("wheel event over stats pane not handled")
	}
	if modal.statsBox == nil || modal.statsBox.Offset <= 0 {
		t.Fatalf("stats offset not advanced after wheel scroll")
	}
}

func TestOptionsModalOptionsScrollbarDragScrolls(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatalf("init simulation screen: %v", err)
	}
	defer scr.Fini()
	scr.SetSize(70, 12)

	entries := make([]OptionsModalEntry, 0, 30)
	for i := 0; i < 30; i++ {
		entries = append(entries, OptionsModalEntry{
			Label:  fmt.Sprintf("Entry %d", i),
			Action: func() {},
		})
	}
	modal := NewOptionsModal("many", entries)
	modal.Draw(scr, Rect{X: 0, Y: 0, W: 70, H: 12})
	if modal.optionsScrollbarRect.H == 0 {
		t.Fatalf("expected options scrollbar for long list")
	}

	dragX := modal.optionsScrollbarRect.X
	dragY := modal.optionsScrollbarRect.Y + modal.optionsScrollbarRect.H - 1
	_ = modal.HandleEvent(tcell.NewEventMouse(dragX, dragY, tcell.Button1, tcell.ModNone))
	if modal.optionsOffset == 0 {
		t.Fatalf("expected options offset to increase after scrollbar drag start")
	}
}
