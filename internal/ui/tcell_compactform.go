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
	UseBoundary      bool
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

	// Cached values. The original implementation recomputed token
	// estimates and the boundary preview on every Draw, which made the
	// form unusably sluggish on large transcripts because tiktoken was
	// re-encoding the entire chain at frame rate. The cache invalidates
	// only when an input the calculation depends on changes.
	cacheReady     bool
	cachedBoundary int  // BoundaryPercent the cache was built for
	cachedUse      bool // UseBoundary the cache was built for
	cachedBefore   int
	cachedAfter    int
	cachedSaved    int
	cachedSavedPct int
	cachedTotal    int
	cachedTarget   int
	cachedVisible  int
	cachedNumComp  int
	cachedPreview  []transcript.PreviewMessage
}

// NewCompactForm constructs a form with defaults (no boundary; only strip
// options apply). The user must opt-in to a boundary cut by checking the
// "Set boundary" checkbox.
func NewCompactForm(sessionName string, allLines []string, chainLines []int) *CompactForm {
	return &CompactForm{
		SessionName:     sessionName,
		AllLines:        allLines,
		ChainLines:      chainLines,
		UseBoundary:     false,
		BoundaryPercent: 50,
	}
}

// fields enumerates focusable controls in order. Tab cycles through these.
// The slider field is only included when UseBoundary is true so Tab does
// not stop on a hidden control.
func (f *CompactForm) fields() []string {
	base := []string{"chk_use_boundary"}
	if f.UseBoundary {
		base = append(base, "slider")
	}
	base = append(base,
		"chk_tool_results",
		"chk_thinking",
		"chk_images",
		"chk_large_inputs",
		"btn_apply",
		"btn_dry_run",
		"btn_cancel",
	)
	return base
}

// fieldIndex returns the position of name in fields(), or -1 if absent.
func (f *CompactForm) fieldIndex(name string) int {
	for i, f := range f.fields() {
		if f == name {
			return i
		}
	}
	return -1
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

	f.ensureCache()

	y := inner.Y

	drawString(scr, inner.X, y, StyleMuted,
		fmt.Sprintf("Chain: %d entries   Tokens: ~%s   Compactions: %d",
			f.cachedTotal, fmtTokens(f.cachedBefore), f.cachedNumComp),
		inner.W)
	y++
	y++

	useFocus := f.focus == f.fieldIndex("chk_use_boundary")
	y += drawCheckbox(scr, inner, y, "Set boundary (drop messages before a cut point)", f.UseBoundary, useFocus)
	y++

	if f.UseBoundary {
		sliderFocus := f.focus == f.fieldIndex("slider")
		drawString(scr, inner.X, y, labelStyle(sliderFocus),
			fmt.Sprintf("Boundary  keep %3d%% visible  (%d entries, ~%s tokens)",
				f.BoundaryPercent, f.cachedVisible, fmtTokens(f.cachedAfter)),
			inner.W)
		y++
		drawSlider(scr, inner.X, y, inner.W, f.BoundaryPercent, sliderFocus)
		y++
		y++
	}

	drawString(scr, inner.X, y, StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true),
		"Strip options", inner.W)
	y++
	y += drawCheckbox(scr, inner, y, "Strip tool_result bodies (stubs)", f.StripToolResults, f.focus == f.fieldIndex("chk_tool_results"))
	y += drawCheckbox(scr, inner, y, "Strip assistant thinking blocks", f.StripThinking, f.focus == f.fieldIndex("chk_thinking"))
	y += drawCheckbox(scr, inner, y, "Strip image blocks (fix dimension errors)", f.StripImages, f.focus == f.fieldIndex("chk_images"))
	y += drawCheckbox(scr, inner, y, "Truncate tool_use inputs > 1 KB", f.StripLargeInputs, f.focus == f.fieldIndex("chk_large_inputs"))
	y++

	if f.UseBoundary {
		drawString(scr, inner.X, y, StyleSubtext,
			fmt.Sprintf("Projected: ~%s tokens saved (%d%% smaller)",
				fmtTokens(f.cachedSaved), f.cachedSavedPct),
			inner.W)
		y++
		y++

		drawString(scr, inner.X, y, StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true),
			fmt.Sprintf("First 5 user messages after boundary (of %d visible)", f.cachedVisible), inner.W)
		y++
		if len(f.cachedPreview) == 0 {
			drawString(scr, inner.X, y, StyleMuted, "  (no user messages in visible range)", inner.W)
			y++
		} else {
			for i, p := range f.cachedPreview {
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
	}

	btnRow := box.Y + box.H - 2
	f.drawButtons(scr, inner.X, btnRow, inner.W)

	hint := "  tab next · space toggle · enter apply · esc cancel"
	drawString(scr, box.X+2, box.Y+box.H-1, StyleMuted, hint, box.W-4)
}

// ensureCache (re)computes the expensive token and preview values when an
// input that affects them changes. Without this cache the form invokes
// tiktoken across the entire chain on every redraw, which makes the form
// unusably slow on large transcripts.
func (f *CompactForm) ensureCache() {
	if f.cacheReady && f.cachedBoundary == f.BoundaryPercent && f.cachedUse == f.UseBoundary {
		return
	}
	total := len(f.ChainLines)
	targetStep := total
	if f.UseBoundary {
		targetStep = total - (total * f.BoundaryPercent / 100)
		if targetStep < 0 {
			targetStep = 0
		}
		if targetStep > total {
			targetStep = total
		}
	}
	visibleCount := total - targetStep

	beforeTok, _ := transcript.EstimateTokens(f.AllLines, f.ChainLines)
	afterTok := beforeTok
	if f.UseBoundary && total > 0 {
		afterTok, _ = transcript.EstimateTokens(f.AllLines, f.ChainLines[targetStep:])
	}
	savedTok := beforeTok - afterTok
	savedPct := 0
	if beforeTok > 0 {
		savedPct = savedTok * 100 / beforeTok
	}

	var preview []transcript.PreviewMessage
	if f.UseBoundary {
		preview = transcript.PreviewMessages(f.AllLines, f.ChainLines, targetStep, 5)
	}

	f.cachedBoundary = f.BoundaryPercent
	f.cachedUse = f.UseBoundary
	f.cachedBefore = beforeTok
	f.cachedAfter = afterTok
	f.cachedSaved = savedTok
	f.cachedSavedPct = savedPct
	f.cachedTotal = total
	f.cachedTarget = targetStep
	f.cachedVisible = visibleCount
	f.cachedNumComp = len(transcript.FindBoundaries(f.AllLines))
	f.cachedPreview = preview
	f.cacheReady = true
}

// invalidateCache forces the next ensureCache call to recompute. Called
// from event handlers that change inputs the cache depends on.
func (f *CompactForm) invalidateCache() {
	f.cacheReady = false
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

// focusedField returns the name of the currently focused field, or "".
func (f *CompactForm) focusedField() string {
	fs := f.fields()
	if f.focus < 0 || f.focus >= len(fs) {
		return ""
	}
	return fs[f.focus]
}

// adjust tweaks the slider when it has focus. Returns true if consumed.
func (f *CompactForm) adjust(delta int) bool {
	if f.focusedField() != "slider" {
		return false
	}
	f.BoundaryPercent = clamp(f.BoundaryPercent+delta, 1, 100)
	f.invalidateCache()
	return true
}

// toggle flips the currently focused checkbox.
func (f *CompactForm) toggle() bool {
	switch f.focusedField() {
	case "chk_use_boundary":
		f.UseBoundary = !f.UseBoundary
		f.invalidateCache()
		return true
	case "chk_tool_results":
		f.StripToolResults = !f.StripToolResults
		return true
	case "chk_thinking":
		f.StripThinking = !f.StripThinking
		return true
	case "chk_images":
		f.StripImages = !f.StripImages
		return true
	case "chk_large_inputs":
		f.StripLargeInputs = !f.StripLargeInputs
		return true
	}
	return false
}

// activate handles Enter on the focused control. Enter on any non-button,
// non-slider control applies the form (the most common action). Enter on a
// button activates that button. Enter on the slider is a no-op so the
// slider stays adjustable without accidentally submitting.
func (f *CompactForm) activate() bool {
	switch f.focusedField() {
	case "slider":
		return false
	case "btn_apply":
		f.finish(f.choices(true, false))
		return true
	case "btn_dry_run":
		f.finish(f.choices(false, true))
		return true
	case "btn_cancel":
		f.finish(CompactChoices{Cancelled: true})
		return true
	default:
		// Any focused checkbox: Enter applies the form. The user already
		// uses Space to toggle a checkbox; Enter is the more natural
		// "I'm done, run it" key.
		f.finish(f.choices(true, false))
		return true
	}
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
// The transcript is loaded on a background goroutine so the open is
// instantaneous on large transcripts. The form renders in a "loading"
// state until the chain arrives, then re-attaches.
func (a *App) openRichCompactForm(sess *session.Session) {
	if sess == nil || sess.Metadata.TranscriptPath == "" {
		return
	}
	form := NewCompactForm(sess.Name, nil, nil)
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

	go func(path string) {
		chain, _, all, err := transcript.WalkChain(path)
		if err != nil {
			return
		}
		form.AllLines = all
		form.ChainLines = chain
		form.invalidateCache()
		if a.screen != nil {
			a.screen.PostEvent(tcell.NewEventInterrupt(nil))
		}
	}(sess.Metadata.TranscriptPath)
}
