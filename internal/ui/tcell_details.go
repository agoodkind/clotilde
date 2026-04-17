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

// DetailsView renders a two column details pane with identity/stats on
// the left and recent messages on the right. It consumes a SessionDetail
// plus a stats cache lookup, produced at populate time.
type DetailsView struct {
	Left  *TextBox
	Right *TextBox

	Rect Rect
}

// NewDetailsView constructs a details pane.
func NewDetailsView() *DetailsView {
	return &DetailsView{
		Left:  &TextBox{Wrap: false, TitleStyle: StyleMuted},
		Right: &TextBox{Wrap: true, TitleStyle: StyleMuted},
	}
}

// Set populates the left and right columns from a session and detail.
func (d *DetailsView) Set(sess *session.Session, detail SessionDetail, statsCache map[string]*transcript.CompactQuickStats) {
	d.Left.Title = "DETAILS"
	d.Right.Title = ""
	d.Left.SetSegments(d.buildLeft(sess, detail, statsCache))
	d.Right.SetSegments(d.buildRight(sess, detail))
}

func (d *DetailsView) buildLeft(sess *session.Session, detail SessionDetail, statsCache map[string]*transcript.CompactQuickStats) [][]TextSegment {
	var out [][]TextSegment
	nameStyle := StyleDefault.Bold(true)
	out = append(out, []TextSegment{{Text: sess.Name, Style: nameStyle}})
	if sess.Metadata.Context != "" {
		out = append(out, []TextSegment{{Text: sess.Metadata.Context, Style: StyleMuted}})
	}
	out = append(out, []TextSegment{})

	kv := func(k, v string) []TextSegment {
		return []TextSegment{
			{Text: fmt.Sprintf("%-12s", k+":"), Style: StyleDefault.Bold(true)},
			{Text: v, Style: StyleSubtext},
		}
	}
	out = append(out, kv("Model", detail.Model))
	out = append(out, kv("Workspace", shortPath(sess.Metadata.WorkspaceRoot)))
	if sess.Metadata.IsForkedSession {
		out = append(out, kv("Type", "fork of "+sess.Metadata.ParentSession))
	}
	out = append(out, []TextSegment{})

	out = append(out, kv("Created", sess.Metadata.Created.Format("2006-01-02 15:04")))
	out = append(out, kv("Last used", util.FormatRelativeTime(sess.Metadata.LastAccessed)))
	out = append(out, []TextSegment{})

	if sess.Metadata.TranscriptPath != "" {
		if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
			mb := float64(info.Size()) / (1024 * 1024)
			out = append(out, kv("Transcript", fmt.Sprintf("%.1f MB", mb)))
		}
	}
	if qs, ok := statsCache[sess.Metadata.TranscriptPath]; ok {
		out = append(out, kv("Tokens", "~"+fmtTokens(qs.EstimatedTokens)))
		out = append(out, kv("Compactions", fmt.Sprintf("%d", qs.Compactions)))
		out = append(out, kv("In context", fmt.Sprintf("%s entries", fmtInt(qs.EntriesInContext))))
		if qs.Compactions > 0 && !qs.LastCompactTime.IsZero() {
			out = append(out, kv("Last compact", util.FormatRelativeTime(qs.LastCompactTime)))
		}
		out = append(out, kv("Total", fmt.Sprintf("%s entries", fmtInt(qs.TotalEntries))))
	} else if sess.Metadata.TranscriptPath != "" {
		out = append(out, []TextSegment{{Text: "Computing stats...", Style: StyleMuted}})
	}
	return out
}

func (d *DetailsView) buildRight(sess *session.Session, detail SessionDetail) [][]TextSegment {
	var out [][]TextSegment
	if len(detail.Messages) > 0 {
		out = append(out, []TextSegment{{Text: "Last exchange:", Style: StyleDefault.Bold(true)}})
		out = append(out, []TextSegment{})
		for _, msg := range detail.Messages {
			role := "You:"
			roleStyle := StyleDefault.Foreground(ColorSuccess).Bold(true)
			if msg.Role == "assistant" {
				role = "Claude:"
				roleStyle = StyleDefault.Foreground(ColorAccent).Bold(true)
			}
			text := strings.ReplaceAll(msg.Text, "\n", " ")
			if runeCount(text) > 180 {
				text = string([]rune(text)[:177]) + "..."
			}
			out = append(out, []TextSegment{
				{Text: "  " + role + " ", Style: roleStyle},
				{Text: text, Style: StyleDefault},
			})
		}
		out = append(out, []TextSegment{})
	}

	dim := StyleMuted
	out = append(out, []TextSegment{{Text: "UUID: " + sess.Metadata.SessionID, Style: dim}})
	if len(sess.Metadata.PreviousSessionIDs) > 0 {
		out = append(out, []TextSegment{{Text: fmt.Sprintf("Previous: %d session(s)", len(sess.Metadata.PreviousSessionIDs)), Style: dim}})
	}
	out = append(out, []TextSegment{{Text: "clotilde resume " + sess.Name, Style: dim}})
	out = append(out, []TextSegment{{Text: "claude --resume " + sess.Metadata.SessionID, Style: dim}})
	return out
}

// Draw splits r into a left and right column and renders each TextBox.
func (d *DetailsView) Draw(scr tcell.Screen, r Rect) {
	d.Rect = r
	if r.W <= 0 || r.H <= 0 {
		return
	}

	// Top border line spanning the full width
	borderStyle := StyleDefault.Foreground(ColorBorder)
	for x := r.X; x < r.X+r.W; x++ {
		scr.SetContent(x, r.Y, '─', nil, borderStyle)
	}

	// Content below the divider
	inner := Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 1}
	leftW := inner.W / 2
	rightX := inner.X + leftW + 1
	rightW := inner.W - leftW - 1
	if rightW < 10 {
		// Single column fallback on narrow terminals
		d.Left.Draw(scr, inner)
		return
	}
	d.Left.Draw(scr, Rect{X: inner.X, Y: inner.Y, W: leftW, H: inner.H})
	// Column separator
	for y := inner.Y; y < inner.Y+inner.H; y++ {
		scr.SetContent(inner.X+leftW, y, '│', nil, borderStyle)
	}
	d.Right.Draw(scr, Rect{X: rightX, Y: inner.Y, W: rightW, H: inner.H})
}

// HandleEvent delegates to the left textbox when the pane has keyboard focus.
// For now keys flow to the table directly; the details pane is visually informative only.
func (d *DetailsView) HandleEvent(ev tcell.Event) bool { return false }

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
