package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// DetailsFocus identifies which sub-pane of the details view owns keyboard
// focus. Only matters for scrolling: keys routed to the focused pane advance
// that pane's scroll offset.
type DetailsFocus int

const (
	DetailsFocusNone DetailsFocus = iota
	DetailsFocusLeft
	DetailsFocusRight
)

// DetailsView renders a two column details pane. The left column holds
// session stats; the right column holds the full conversation. Both are
// independently scrollable. A parent (App) shifts focus between them.
type DetailsView struct {
	Left  *TextBox
	Right *TextBox
	Focus DetailsFocus

	Rect      Rect
	LeftRect  Rect // last-drawn rect for the left pane, used for mouse hit-testing
	RightRect Rect // last-drawn rect for the right pane

	// FormatTokens lets the parent App inject its tokenizer-aware
	// formatter. The default falls back to the local approximate
	// formatter so the widget remains usable in isolation.
	FormatTokens func(sess *session.Session, estimated int) string
}

// formatTokens delegates to the injected formatter or falls back to a
// safe approximate format.
func (d *DetailsView) formatTokens(sess *session.Session, estimated int) string {
	if d.FormatTokens != nil {
		return d.FormatTokens(sess, estimated)
	}
	return fmtTokenCount(estimated, false)
}

// NewDetailsView constructs a details pane.
func NewDetailsView() *DetailsView {
	return &DetailsView{
		Left:  &TextBox{Wrap: false, TitleStyle: StyleMuted},
		Right: &TextBox{Wrap: true, TitleStyle: StyleMuted},
	}
}

// Set populates both columns from a session and detail payload.
func (d *DetailsView) Set(sess *session.Session, detail SessionDetail, statsCache map[string]*transcript.CompactQuickStats) {
	d.Left.Title = " STATS "
	d.Right.Title = " MESSAGES "
	d.Left.SetSegments(d.buildLeft(sess, detail, statsCache))
	d.Right.SetSegments(d.buildRight(sess, detail))
	d.Left.Offset = 0
	d.Right.Offset = 0
}

// SetFocus moves keyboard focus between Left, Right, or None.
func (d *DetailsView) SetFocus(f DetailsFocus) {
	d.Focus = f
	d.Left.Focused = f == DetailsFocusLeft
	d.Right.Focused = f == DetailsFocusRight
}

// buildLeft composes the stats column as a slice of styled logical lines.
// The parent TextBox handles wrapping and scrolling.
func (d *DetailsView) buildLeft(sess *session.Session, detail SessionDetail, statsCache map[string]*transcript.CompactQuickStats) [][]TextSegment {
	var out [][]TextSegment

	// Header: name + optional context (no label, just prominent).
	out = append(out, []TextSegment{{Text: sess.Name, Style: StyleDefault.Bold(true)}})
	if sess.Metadata.Context != "" {
		ctx := sess.Metadata.Context
		if n := runeCount(ctx); n > 120 {
			ctx = string([]rune(ctx)[:117]) + "..."
		}
		out = append(out, []TextSegment{{Text: ctx, Style: StyleMuted}})
	}
	out = append(out, []TextSegment{})

	// section writes a bold heading line.
	section := func(title string) {
		out = append(out, []TextSegment{{Text: title, Style: StyleDefault.Foreground(ColorMuted).Bold(true).Underline(true)}})
	}
	kv := func(k, v string) {
		out = append(out, []TextSegment{
			{Text: fmt.Sprintf("  %-14s", k), Style: StyleSubtext},
			{Text: v, Style: StyleDefault},
		})
	}

	section("Identity")
	kv("Model", detail.Model)
	kv("Basedir", shortPath(sess.Metadata.WorkspaceRoot))
	if sess.Metadata.WorkDir != "" && sess.Metadata.WorkDir != sess.Metadata.WorkspaceRoot {
		kv("Work dir", shortPath(sess.Metadata.WorkDir))
	}
	if sess.Metadata.IsForkedSession {
		kv("Type", "fork of "+sess.Metadata.ParentSession)
	}
	if sess.Metadata.IsIncognito {
		kv("Type", "incognito (auto-delete on exit)")
	}
	out = append(out, []TextSegment{})

	section("Timing")
	kv("Created", sess.Metadata.Created.Format("2006-01-02 15:04"))
	kv("Last used", util.FormatRelativeTime(sess.Metadata.LastAccessed))
	age := util.FormatRelativeTime(sess.Metadata.Created)
	kv("Age", age)
	out = append(out, []TextSegment{})

	section("Transcript")
	if sess.Metadata.TranscriptPath != "" {
		if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
			mb := float64(info.Size()) / (1024 * 1024)
			kv("Size", fmt.Sprintf("%.2f MB", mb))
		}
	}
	if qs, ok := statsCache[sess.Metadata.TranscriptPath]; ok {
		kv("Tokens", d.formatTokens(sess, qs.EstimatedTokens))
		kv("Compactions", fmt.Sprintf("%d", qs.Compactions))
		kv("In context", fmt.Sprintf("%s entries", fmtInt(qs.EntriesInContext)))
		if qs.Compactions > 0 && !qs.LastCompactTime.IsZero() {
			kv("Last compact", util.FormatRelativeTime(qs.LastCompactTime))
		}
		kv("Total", fmt.Sprintf("%s entries", fmtInt(qs.TotalEntries)))
	} else if sess.Metadata.TranscriptPath != "" {
		out = append(out, []TextSegment{{Text: "  computing stats...", Style: StyleMuted}})
	}
	out = append(out, []TextSegment{})

	if len(detail.AllMessages) > 0 {
		section("Conversation")
		users, assistants := 0, 0
		var firstTS, lastTS string
		for i, m := range detail.AllMessages {
			switch m.Role {
			case "user":
				users++
			case "assistant":
				assistants++
			}
			if i == 0 && !m.Timestamp.IsZero() {
				firstTS = m.Timestamp.Local().Format("2006-01-02 15:04")
			}
			if i == len(detail.AllMessages)-1 && !m.Timestamp.IsZero() {
				lastTS = m.Timestamp.Local().Format("2006-01-02 15:04")
			}
		}
		kv("Messages", fmt.Sprintf("%d  (%d user, %d assistant)", users+assistants, users, assistants))
		if firstTS != "" {
			kv("First message", firstTS)
		}
		if lastTS != "" {
			kv("Last message", lastTS)
		}
		out = append(out, []TextSegment{})
	}

	if len(detail.Tools) > 0 {
		section("Top tools")
		for _, t := range detail.Tools {
			out = append(out, []TextSegment{
				{Text: fmt.Sprintf("  %-14s", t.Name), Style: StyleSubtext},
				{Text: fmt.Sprintf("%d", t.Count), Style: StyleDefault},
			})
		}
		out = append(out, []TextSegment{})
	}

	section("Identifiers")
	kv("UUID", sess.Metadata.SessionID)
	if len(sess.Metadata.PreviousSessionIDs) > 0 {
		kv("Previous", fmt.Sprintf("%d prior UUID(s)", len(sess.Metadata.PreviousSessionIDs)))
	}
	out = append(out, []TextSegment{})

	section("Resume")
	out = append(out, []TextSegment{{Text: "  clotilde resume " + sess.Name, Style: StyleMuted}})
	out = append(out, []TextSegment{{Text: "  claude --resume " + sess.Metadata.SessionID, Style: StyleMuted}})

	return out
}

// buildRight renders the full conversation. Each message gets a role tag
// and a timestamp. Long bodies are wrapped by the parent TextBox because
// its Wrap flag is on.
func (d *DetailsView) buildRight(sess *session.Session, detail SessionDetail) [][]TextSegment {
	src := detail.AllMessages
	if len(src) == 0 && len(detail.Messages) > 0 {
		src = detail.Messages
	}

	if len(src) == 0 {
		return [][]TextSegment{{{Text: "  (no visible messages)", Style: StyleMuted}}}
	}

	// Latest message first so the user reads the most recent turn at the
	// top without scrolling. We copy rather than reverse in place because
	// the source slice is shared with the cache.
	msgs := make([]DetailMessage, len(src))
	for i, m := range src {
		msgs[len(src)-1-i] = m
	}

	var out [][]TextSegment

	userTag := StyleDefault.Foreground(ColorSuccess).Bold(true)
	userBody := StyleDefault.Foreground(ColorText)
	assistantTag := StyleDefault.Foreground(ColorAccent).Bold(true)
	assistantBody := StyleDefault.Foreground(ColorSubtext)
	tsStyle := StyleDefault.Foreground(ColorMuted)

	for i, m := range msgs {
		tag := "You"
		tagStyle := userTag
		bodyStyle := userBody
		if m.Role == "assistant" {
			tag = "Claude"
			tagStyle = assistantTag
			bodyStyle = assistantBody
		}

		ts := ""
		if !m.Timestamp.IsZero() {
			ts = m.Timestamp.Local().Format("01-02 15:04")
		}

		header := []TextSegment{
			{Text: fmt.Sprintf("▎%-6s", tag), Style: tagStyle},
		}
		if ts != "" {
			header = append(header, TextSegment{Text: " " + ts, Style: tsStyle})
		}
		out = append(out, header)

		// Body lines: split on existing newlines so the TextBox wrapping does
		// not accidentally glue them together. Blank line after each message.
		body := strings.ReplaceAll(m.Text, "\r", "")
		for _, line := range strings.Split(body, "\n") {
			out = append(out, []TextSegment{{Text: "  " + line, Style: bodyStyle}})
		}
		if i != len(msgs)-1 {
			out = append(out, []TextSegment{})
		}
	}
	return out
}

// Draw splits r into a left and right column and renders each TextBox.
// A thin horizontal rule separates the details view from the table above.
// A single vertical rule splits the two sub-panes.
func (d *DetailsView) Draw(scr tcell.Screen, r Rect) {
	d.Rect = r
	if r.W <= 0 || r.H <= 0 {
		return
	}

	// Wipe the entire details rect first. The sub-panes each clear their
	// own inner rects, but the one-column left and right margins plus the
	// divider column are outside those rects. Without a full clear, stale
	// pixels from the previous draw (for example table content before the
	// user opened details, or old scrollbar glyphs when content length
	// changed) leak into those margin columns.
	clearRect(scr, r)

	borderStyle := StyleDefault.Foreground(ColorBorder)
	for x := r.X; x < r.X+r.W; x++ {
		scr.SetContent(x, r.Y, '─', nil, borderStyle)
	}

	inner := Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 1}
	if inner.H <= 0 {
		return
	}

	// Split 40/60 so the right pane has more room for message bodies.
	leftW := inner.W * 40 / 100
	if leftW < 28 {
		leftW = imin(28, inner.W-1)
	}
	rightX := inner.X + leftW + 1
	rightW := inner.W - leftW - 1
	if rightW < 15 {
		d.Left.Draw(scr, inner)
		return
	}

	d.LeftRect = Rect{X: inner.X, Y: inner.Y, W: leftW, H: inner.H}
	d.RightRect = Rect{X: rightX, Y: inner.Y, W: rightW, H: inner.H}
	d.Left.Draw(scr, d.LeftRect)
	for y := inner.Y; y < inner.Y+inner.H; y++ {
		scr.SetContent(inner.X+leftW, y, '│', nil, borderStyle)
	}
	d.Right.Draw(scr, d.RightRect)

	// Focus indicator: paint the entire top row of the focused pane in
	// accent colors so the user always sees which pane consumes arrow
	// keys. The non-focused pane keeps its muted title.
	focusStyle := tcell.StyleDefault.Background(ColorAccent).Foreground(tcell.ColorBlack).Bold(true)
	idleStyle := StyleSubtext.Bold(true)
	switch d.Focus {
	case DetailsFocusLeft:
		fillRow(scr, inner.X, inner.Y, leftW, focusStyle)
		drawString(scr, inner.X+1, inner.Y, focusStyle, "STATS  (focused, ↑↓ to scroll)", leftW-1)
		drawString(scr, rightX+1, inner.Y, idleStyle, "MESSAGES  (tab to focus)", rightW-1)
	case DetailsFocusRight:
		fillRow(scr, rightX, inner.Y, rightW, focusStyle)
		drawString(scr, rightX+1, inner.Y, focusStyle, "MESSAGES  (focused, ↑↓ to scroll)", rightW-1)
		drawString(scr, inner.X+1, inner.Y, idleStyle, "STATS  (tab to focus)", leftW-1)
	default:
		drawString(scr, inner.X+1, inner.Y, idleStyle, "STATS  (tab to focus)", leftW-1)
		drawString(scr, rightX+1, inner.Y, idleStyle, "MESSAGES  (tab to focus)", rightW-1)
	}
}

// HandleEvent forwards keyboard events to the focused sub-pane only.
// The parent App decides which pane is focused.
func (d *DetailsView) HandleEvent(ev tcell.Event) bool {
	switch d.Focus {
	case DetailsFocusLeft:
		return d.Left.HandleEvent(ev)
	case DetailsFocusRight:
		return d.Right.HandleEvent(ev)
	}
	return false
}

// ---------------- formatting helpers ----------------

func fmtTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// fmtTokenCount formats a token value with the "approximate" prefix
// applied only when the source is the local tiktoken estimate. Counts
// returned by the Claude API are exact, so the prefix would be
// misleading. Callers pass exact=true when the value came from the API.
func fmtTokenCount(n int, exact bool) string {
	if exact {
		return fmtTokens(n)
	}
	return "~" + fmtTokens(n)
}

func fmtInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return b.String()
}
