package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// CompactChoices carries the final selections from the compact form.
// It mirrors the legacy BubbleTea CompactChoices shape so cmd/compact can
// keep its signature unchanged.
type CompactChoices struct {
	BoundaryPercent  int // percent of chain to keep VISIBLE after boundary, 1..100
	StripToolResults bool
	StripThinking    bool
	StripImages      bool
	StripLargeInputs bool
	Applied          bool
	DryRun           bool
	Cancelled        bool
}

// CompactForm is the in-TUI overlay for the compact command. It exposes a
// slider, four checkboxes, and three buttons. Live estimates update as the
// user manipulates the form.
type CompactForm struct {
	SessionName string

	// AllLines is the full transcript. ChainLines is the active chain.
	// Both are optional; with them, the form renders a live preview of
	// post-boundary messages and a projected token delta.
	AllLines   []string
	ChainLines []int

	// Current values. Initialize before first Draw.
	BoundaryPercent  int
	StripToolResults bool
	StripThinking    bool
	StripImages      bool
	StripLargeInputs bool

	// Focus cursor. Indexes into the flat fields list defined in the
	// fields() method.
	focus int

	Rect Rect

	// OnDone receives the final selections. Exactly one of Applied/DryRun/
	// Cancelled is true.
	OnDone func(CompactChoices)
}

// NewCompactForm constructs a form with defaults (50% visible, no strips).
func NewCompactForm(sessionName string, allLines []string, chainLines []int) *CompactForm {
	return &CompactForm{
		SessionName:     sessionName,
		AllLines:        allLines,
		ChainLines:      chainLines,
		BoundaryPercent: 50,
	}
}

// fields enumerates focusable controls in order. Tab cycles through these.
func (f *CompactForm) fields() []string {
	return []string{
		"slider",
		"chk_tool_results",
		"chk_thinking",
		"chk_images",
		"chk_large_inputs",
		"btn_apply",
		"btn_dry_run",
		"btn_cancel",
	}
}

// ---------------- Draw ----------------

func (f *CompactForm) Draw(scr tcell.Screen, r Rect) {
	// Size the box: roughly 72 columns, 22 rows, clamped to the screen.
	w := 78
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 24
	if h > r.H-2 {
		h = r.H - 2
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	f.Rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorBorder)

	// Title
	title := fmt.Sprintf(" Compact: %s ", f.SessionName)
	drawString(scr, box.X+2, box.Y, StyleDefault.Foreground(ColorAccent).Bold(true), title, box.W-4)

	inner := Rect{X: box.X + 2, Y: box.Y + 1, W: box.W - 4, H: box.H - 2}

	// Pre-compute chain stats for the live preview.
	total := len(f.ChainLines)
	targetStep := total - (total * f.BoundaryPercent / 100)
	if targetStep < 0 {
		targetStep = 0
	}
	if targetStep > total {
		targetStep = total
	}
	visibleCount := total - targetStep

	// Before/after token estimates (best-effort, tiktoken).
	beforeTok, _ := transcript.EstimateTokens(f.AllLines, f.ChainLines)
	afterTok := 0
	if total > 0 {
		afterTok, _ = transcript.EstimateTokens(f.AllLines, f.ChainLines[targetStep:])
	}
	savedTok := beforeTok - afterTok
	savedPct := 0
	if beforeTok > 0 {
		savedPct = savedTok * 100 / beforeTok
	}

	y := inner.Y

	// Chain summary
	drawString(scr, inner.X, y, StyleMuted,
		fmt.Sprintf("Chain: %d entries   Tokens: ~%s   Compactions: %d",
			total, fmtTokens(beforeTok), len(transcript.FindBoundaries(f.AllLines))),
		inner.W)
	y++
	y++

	// Slider
	sliderFocus := f.focus == 0
	drawString(scr, inner.X, y, labelStyle(sliderFocus),
		fmt.Sprintf("Boundary  keep %3d%% visible  (%d entries, ~%s tokens)",
			f.BoundaryPercent, visibleCount, fmtTokens(afterTok)),
		inner.W)
	y++
	drawSlider(scr, inner.X, y, inner.W, f.BoundaryPercent, sliderFocus)
	y++
	y++

	// Strip options
	drawString(scr, inner.X, y, StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true),
		"Strip options", inner.W)
	y++
	y += drawCheckbox(scr, inner, y, "Strip tool_result bodies (stubs)", f.StripToolResults, f.focus == 1)
	y += drawCheckbox(scr, inner, y, "Strip assistant thinking blocks", f.StripThinking, f.focus == 2)
	y += drawCheckbox(scr, inner, y, "Strip image blocks (fix dimension errors)", f.StripImages, f.focus == 3)
	y += drawCheckbox(scr, inner, y, "Truncate tool_use inputs > 1 KB", f.StripLargeInputs, f.focus == 4)
	y++

	// Projected savings
	drawString(scr, inner.X, y, StyleSubtext,
		fmt.Sprintf("Projected: ~%s tokens saved (%d%% smaller)",
			fmtTokens(savedTok), savedPct),
		inner.W)
	y++
	y++

	// Preview
	drawString(scr, inner.X, y, StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true),
		fmt.Sprintf("First 5 user messages after boundary (of %d visible)", visibleCount), inner.W)
	y++
	preview := transcript.PreviewMessages(f.AllLines, f.ChainLines, targetStep, 5)
	if len(preview) == 0 {
		drawString(scr, inner.X, y, StyleMuted, "  (no user messages in visible range)", inner.W)
		y++
	} else {
		for i, p := range preview {
			ts := "     --     "
			if !p.Timestamp.IsZero() {
				ts = p.Timestamp.Local().Format("01-02 15:04")
			}
			line := fmt.Sprintf("  %d. [%s] %s", i+1, ts, p.Text)
			if runeCount(line) > inner.W {
				line = string([]rune(line)[:inner.W-3]) + "..."
			}
			drawString(scr, inner.X, y, StyleSubtext, line, inner.W)
			y++
		}
	}

	// Button row at the bottom of the inner area.
	btnRow := box.Y + box.H - 2
	f.drawButtons(scr, inner.X, btnRow, inner.W)

	// Footer hint
	hint := "  tab next · space toggle · ←→ slider · enter activate · esc cancel"
	drawString(scr, box.X+2, box.Y+box.H-1, StyleMuted, hint, box.W-4)
}

func labelStyle(focused bool) tcell.Style {
	if focused {
		return StyleDefault.Foreground(ColorAccent).Bold(true)
	}
	return StyleDefault.Bold(true)
}

// drawSlider paints a horizontal slider filled to pct percent of width.
func drawSlider(scr tcell.Screen, x, y, w, pct int, focused bool) {
	if w < 10 {
		return
	}
	trackStart := x
	trackEnd := x + w - 1
	trackLen := trackEnd - trackStart - 1
	filled := trackLen * clamp(pct, 0, 100) / 100

	trackStyle := StyleDefault.Foreground(ColorMuted)
	fillStyle := StyleDefault.Foreground(ColorAccent)
	if focused {
		fillStyle = fillStyle.Bold(true)
	}

	scr.SetContent(trackStart, y, '[', nil, trackStyle)
	for i := 0; i < trackLen; i++ {
		if i < filled {
			scr.SetContent(trackStart+1+i, y, '=', nil, fillStyle)
		} else {
			scr.SetContent(trackStart+1+i, y, '·', nil, trackStyle)
		}
	}
	if filled > 0 && filled <= trackLen {
		scr.SetContent(trackStart+filled, y, '>', nil, fillStyle)
	}
	scr.SetContent(trackEnd, y, ']', nil, trackStyle)
}

// drawCheckbox paints "[x] label" or "[ ] label". Returns 1 (row count).
func drawCheckbox(scr tcell.Screen, r Rect, y int, label string, checked, focused bool) int {
	mark := "[ ]"
	if checked {
		mark = "[x]"
	}
	cursor := "  "
	style := StyleSubtext
	if focused {
		cursor = "▸ "
		style = StyleDefault.Foreground(ColorAccent).Bold(true)
	}
	line := cursor + mark + " " + label
	drawString(scr, r.X, y, style, line, r.W)
	return 1
}

func (f *CompactForm) drawButtons(scr tcell.Screen, x, y, w int) {
	buttons := []struct {
		label string
		idx   int
		style tcell.Style
	}{
		{"[ Apply ]", 5, StyleDefault.Foreground(ColorSuccess).Bold(true)},
		{"[ Dry run ]", 6, StyleDefault.Foreground(ColorWarning).Bold(true)},
		{"[ Cancel ]", 7, StyleDefault.Foreground(ColorSubtext)},
	}
	total := 0
	for i, b := range buttons {
		if i > 0 {
			total += 2
		}
		total += runeCount(b.label)
	}
	// Right align the button row.
	cx := x + w - total
	if cx < x {
		cx = x
	}
	for i, b := range buttons {
		style := b.style
		if f.focus == b.idx {
			style = style.Reverse(true)
		}
		cx += drawString(scr, cx, y, style, b.label, x+w-cx)
		if i < len(buttons)-1 {
			cx += drawString(scr, cx, y, StyleDefault, "  ", x+w-cx)
		}
	}
}

// drawBoxBorder paints a single-line border around box using color.
func drawBoxBorder(scr tcell.Screen, box Rect, color tcell.Color) {
	style := StyleDefault.Foreground(color)
	scr.SetContent(box.X, box.Y, '┌', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y, '┐', nil, style)
	scr.SetContent(box.X, box.Y+box.H-1, '└', nil, style)
	scr.SetContent(box.X+box.W-1, box.Y+box.H-1, '┘', nil, style)
	for x := box.X + 1; x < box.X+box.W-1; x++ {
		scr.SetContent(x, box.Y, '─', nil, style)
		scr.SetContent(x, box.Y+box.H-1, '─', nil, style)
	}
	for yy := box.Y + 1; yy < box.Y+box.H-1; yy++ {
		scr.SetContent(box.X, yy, '│', nil, style)
		scr.SetContent(box.X+box.W-1, yy, '│', nil, style)
	}
}

// ---------------- Events ----------------

// HandleEvent routes keyboard events to the focused control.
func (f *CompactForm) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		return f.handleKey(e)
	case *tcell.EventMouse:
		x, y := e.Position()
		if f.Rect.Contains(x, y) {
			return true // consume; richer hit-testing could be added
		}
	}
	return false
}

func (f *CompactForm) handleKey(e *tcell.EventKey) bool {
	fields := f.fields()
	switch e.Key() {
	case tcell.KeyEscape:
		f.finish(CompactChoices{Cancelled: true})
		return true
	case tcell.KeyTab, tcell.KeyDown:
		f.focus = (f.focus + 1) % len(fields)
		return true
	case tcell.KeyBacktab, tcell.KeyUp:
		f.focus = (f.focus - 1 + len(fields)) % len(fields)
		return true
	case tcell.KeyLeft:
		return f.adjust(-5)
	case tcell.KeyRight:
		return f.adjust(+5)
	case tcell.KeyEnter:
		return f.activate()
	case tcell.KeyRune:
		switch e.Rune() {
		case ' ':
			return f.toggle()
		case 'h':
			return f.adjust(-5)
		case 'l':
			return f.adjust(+5)
		case 'a', 'A':
			f.finish(f.choices(true, false))
			return true
		case 'd', 'D':
			f.finish(f.choices(false, true))
			return true
		case 'q', 'Q':
			f.finish(CompactChoices{Cancelled: true})
			return true
		}
	}
	return false
}

// adjust tweaks the slider when it has focus. Returns true if consumed.
func (f *CompactForm) adjust(delta int) bool {
	if f.focus != 0 {
		return false
	}
	f.BoundaryPercent = clamp(f.BoundaryPercent+delta, 1, 100)
	return true
}

// toggle flips the currently focused checkbox.
func (f *CompactForm) toggle() bool {
	switch f.focus {
	case 1:
		f.StripToolResults = !f.StripToolResults
		return true
	case 2:
		f.StripThinking = !f.StripThinking
		return true
	case 3:
		f.StripImages = !f.StripImages
		return true
	case 4:
		f.StripLargeInputs = !f.StripLargeInputs
		return true
	}
	return false
}

// activate handles Enter on the focused control.
func (f *CompactForm) activate() bool {
	switch f.focus {
	case 0:
		return false
	case 1, 2, 3, 4:
		return f.toggle()
	case 5:
		f.finish(f.choices(true, false))
		return true
	case 6:
		f.finish(f.choices(false, true))
		return true
	case 7:
		f.finish(CompactChoices{Cancelled: true})
		return true
	}
	return false
}

func (f *CompactForm) choices(applied, dryRun bool) CompactChoices {
	return CompactChoices{
		BoundaryPercent:  f.BoundaryPercent,
		StripToolResults: f.StripToolResults,
		StripThinking:    f.StripThinking,
		StripImages:      f.StripImages,
		StripLargeInputs: f.StripLargeInputs,
		Applied:          applied,
		DryRun:           dryRun,
	}
}

func (f *CompactForm) finish(c CompactChoices) {
	if f.OnDone != nil {
		f.OnDone(c)
	}
}

// ---------------- Glue for the main App ----------------

// openCompactFormFor is called by App when the user presses `c` on a session.
// It loads the chain, builds the form, and wires the callback to ApplyCompact.
func (a *App) openRichCompactForm(sess *session.Session) {
	if sess == nil || sess.Metadata.TranscriptPath == "" {
		return
	}
	chain, _, all, err := transcript.WalkChain(sess.Metadata.TranscriptPath)
	if err != nil {
		return
	}
	form := NewCompactForm(sess.Name, all, chain)
	form.OnDone = func(c CompactChoices) {
		a.overlay = nil
		if c.Cancelled {
			return
		}
		if a.cb.ApplyCompact == nil {
			return
		}
		a.mode = StatusCompact
		a.suspendAndRun(func() {
			_ = a.cb.ApplyCompact(sess, c)
		})
		a.refreshSessions()
	}
	a.overlay = form
	a.mode = StatusCompact
}
