package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// TabEntry is one tab on the top strip. Number is the digit shown in
// front of the label, e.g. "1:Sessions". Click hit testing uses the
// per-tab Rect populated during Draw.
type TabEntry struct {
	Number int
	Label  string
	rect   Rect
}

// TabBarWidget renders a horizontal strip of tabs at the top of the
// screen. The strip uses a bright purple background, mirroring the
// reference screenshot. The active tab inverts so the user always knows
// where they are. The widget owns selection state but defers actions to
// the parent App via OnActivate.
type TabBarWidget struct {
	Tabs   []TabEntry
	Active int

	OnActivate func(idx int)

	rect Rect
}

func NewTabBar(labels []string) *TabBarWidget {
	tabs := make([]TabEntry, len(labels))
	for i, l := range labels {
		tabs[i] = TabEntry{Number: i + 1, Label: l}
	}
	return &TabBarWidget{Tabs: tabs}
}

func (t *TabBarWidget) Draw(scr tcell.Screen, r Rect) {
	t.rect = r
	fillRow(scr, r.X, r.Y, r.W, StyleTabBar)

	x := r.X + 1
	for i := range t.Tabs {
		entry := &t.Tabs[i]
		text := fmt.Sprintf(" %d:%s ", entry.Number, entry.Label)
		style := StyleTabBar.Bold(true)
		if i == t.Active {
			style = StyleTabActive
		}
		used := drawString(scr, x, r.Y, style, text, r.W-x)
		entry.rect = Rect{X: x, Y: r.Y, W: used, H: 1}
		x += used
		x += drawString(scr, x, r.Y, StyleTabBar, "  ", r.W-x)
	}
}

// HandleEvent returns true if the widget consumed a click on a tab.
// Keyboard handling lives in the App so it can decide between sort
// digits and tab digits based on overlay state.
func (t *TabBarWidget) HandleEvent(ev tcell.Event) bool {
	em, ok := ev.(*tcell.EventMouse)
	if !ok {
		return false
	}
	if em.Buttons()&tcell.ButtonPrimary == 0 {
		return false
	}
	x, y := em.Position()
	for i, e := range t.Tabs {
		if e.rect.Contains(x, y) {
			t.Active = i
			if t.OnActivate != nil {
				t.OnActivate(i)
			}
			return true
		}
	}
	return false
}

// SetActive changes the highlighted tab without firing OnActivate.
func (t *TabBarWidget) SetActive(idx int) {
	if idx < 0 || idx >= len(t.Tabs) {
		return
	}
	t.Active = idx
}
