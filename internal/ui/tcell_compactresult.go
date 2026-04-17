package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// CompactResultModal is the post-apply summary that pops up after a
// compaction finishes. It shows what was stripped, by how much, and
// where the backup landed. Pressing Enter or Esc dismisses it.
type CompactResultModal struct {
	SessionName string
	Result      CompactResult
	Err         error
	OnClose     func()

	rect Rect
}

// CompactStatusModal is the brief "Applying..." overlay shown while
// the compact pipeline runs. It is replaced by CompactResultModal as
// soon as the apply finishes.
type CompactStatusModal struct {
	SessionName string
	rect        Rect
}

func NewCompactStatusModal(name string) *CompactStatusModal {
	return &CompactStatusModal{SessionName: name}
}

func (m *CompactStatusModal) Draw(scr tcell.Screen, r Rect) {
	w := 50
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 5
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	m.rect = box
	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorAccent)
	drawString(scr, box.X+2, box.Y+1, StyleDefault.Foreground(ColorAccent).Bold(true), " Compacting "+m.SessionName, box.W-4)
	drawString(scr, box.X+2, box.Y+2, StyleSubtext, "  please wait", box.W-4)
}

func (m *CompactStatusModal) HandleEvent(_ tcell.Event) bool { return true }

// NewCompactResultModal builds a modal scoped to one session result.
func NewCompactResultModal(name string, r CompactResult, err error) *CompactResultModal {
	return &CompactResultModal{SessionName: name, Result: r, Err: err}
}

func (m *CompactResultModal) Draw(scr tcell.Screen, r Rect) {
	w := 70
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 18
	if h > r.H-2 {
		h = r.H - 2
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	m.rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorAccent)

	title := " Compact result: " + m.SessionName + " "
	drawString(scr, box.X+2, box.Y, StyleDefault.Foreground(ColorAccent).Bold(true), title, box.W-4)

	y := box.Y + 2
	if m.Err != nil {
		drawString(scr, box.X+2, y, StyleDefault.Foreground(ColorError).Bold(true), "Failed: "+m.Err.Error(), box.W-4)
		y++
	} else {
		drawString(scr, box.X+2, y, StyleDefault.Foreground(ColorSuccess).Bold(true), "Compaction complete.", box.W-4)
		y += 2

		rows := []struct{ label, value string }{
			{"Backup", shortenForModal(m.Result.BackupPath, box.W-20)},
			{"Bytes", fmt.Sprintf("%d -> %d (delta %s)", m.Result.BeforeBytes, m.Result.AfterBytes, signedBytes(m.Result.AfterBytes-m.Result.BeforeBytes))},
			{"Chain", fmt.Sprintf("%d -> %d entries", m.Result.BeforeChainLines, m.Result.AfterChainLines)},
			{"Boundary moved", boolYesNo(m.Result.BoundaryMoved)},
			{"Stripped total", fmt.Sprintf("%d blocks", m.Result.StrippedTotal)},
			{"  images", fmt.Sprintf("%d (kept last %d)", m.Result.StrippedImages, m.Result.KeptLastImages)},
			{"  tool_results", fmt.Sprintf("%d (kept last %d)", m.Result.StrippedTools, m.Result.KeptLastTools)},
			{"  thinking", fmt.Sprintf("%d (kept last %d)", m.Result.StrippedThinking, m.Result.KeptLastThinking)},
			{"  large_inputs", fmt.Sprintf("%d", m.Result.StrippedLargeIn)},
		}
		for _, r := range rows {
			label := fmt.Sprintf("  %-16s ", r.label)
			drawString(scr, box.X+2, y, StyleSubtext, label, box.W-4)
			drawString(scr, box.X+2+runeCount(label), y, StyleDefault.Foreground(ColorText), r.value, box.W-4-runeCount(label))
			y++
		}
	}

	hint := "  enter / esc close"
	drawString(scr, box.X+2, box.Y+box.H-1, StyleMuted, hint, box.W-4)
}

func (m *CompactResultModal) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		switch e.Key() {
		case tcell.KeyEscape, tcell.KeyEnter:
			if m.OnClose != nil {
				m.OnClose()
			}
			return true
		}
		if e.Rune() == 'q' {
			if m.OnClose != nil {
				m.OnClose()
			}
			return true
		}
	case *tcell.EventMouse:
		x, y := e.Position()
		if !m.rect.Contains(x, y) && e.Buttons() != 0 {
			if m.OnClose != nil {
				m.OnClose()
			}
			return true
		}
	}
	return false
}

// signedBytes pretty-formats a byte delta with a sign prefix so the
// reader instantly sees whether the file grew or shrank.
func signedBytes(delta int64) string {
	if delta >= 0 {
		return fmt.Sprintf("+%d", delta)
	}
	return fmt.Sprintf("%d", delta)
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// shortenForModal trims a long path so it fits the modal width. The
// front of the path is kept because the trailing UUID-and-timestamp
// segment is the most identifying part for backups.
func shortenForModal(s string, max int) string {
	if max <= 8 || len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max+3:]
}
