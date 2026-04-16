package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// SessionDetail holds pre-extracted data for the details pane.
type SessionDetail struct {
	Model    string
	Messages []DetailMessage
}

// DetailMessage is a simplified message for display.
type DetailMessage struct {
	Role string
	Text string
}

// DetailsPane shows rich session information in a two-column layout at the bottom.
type DetailsPane struct {
	*tview.Flex
	leftCol    *tview.TextView
	rightCol   *tview.TextView
	sess       *session.Session
	statsCache map[string]*transcript.CompactQuickStats
}

// NewDetailsPane creates a two-column details pane.
func NewDetailsPane() *DetailsPane {
	left := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true)

	right := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true)

	// Two columns inside a bordered flex
	inner := tview.NewFlex().
		AddItem(left, 0, 1, true).
		AddItem(right, 0, 1, false)

	inner.SetBorder(true).
		SetTitle(" DETAILS (esc close) ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.Color240)

	// Wrap in outer flex for consistent sizing
	outer := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(inner, 0, 1, true)

	return &DetailsPane{
		Flex:       outer,
		leftCol:    left,
		rightCol:   right,
		statsCache: make(map[string]*transcript.CompactQuickStats),
	}
}

// SetStatsCache shares the stats cache from the session table.
func (d *DetailsPane) SetStatsCache(cache map[string]*transcript.CompactQuickStats) {
	d.statsCache = cache
}

// ShowSession populates both columns with session information.
func (d *DetailsPane) ShowSession(sess *session.Session, detail SessionDetail) {
	d.sess = sess
	d.leftCol.Clear()
	d.rightCol.Clear()

	if sess == nil {
		return
	}

	// LEFT COLUMN: identity, timing, stats
	var left strings.Builder

	fmt.Fprintf(&left, "[::b]%s[-:-:-]\n", tview.Escape(sess.Name))
	if sess.Metadata.Context != "" {
		fmt.Fprintf(&left, "[gray]%s[-]\n", tview.Escape(sess.Metadata.Context))
	}
	left.WriteString("\n")

	ws := shortPath(sess.Metadata.WorkspaceRoot)
	fmt.Fprintf(&left, "[::b]Model:[-]       %s\n", detail.Model)
	fmt.Fprintf(&left, "[::b]Workspace:[-]   %s\n", ws)
	if sess.Metadata.IsForkedSession {
		fmt.Fprintf(&left, "[::b]Type:[-]        [yellow]fork of %s[-]\n", sess.Metadata.ParentSession)
	}
	left.WriteString("\n")

	fmt.Fprintf(&left, "[::b]Created:[-]     %s\n", sess.Metadata.Created.Format("2006-01-02 15:04"))
	fmt.Fprintf(&left, "[::b]Last used:[-]   %s\n", util.FormatRelativeTime(sess.Metadata.LastAccessed))
	left.WriteString("\n")

	if sess.Metadata.TranscriptPath != "" {
		if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
			sizeMB := float64(info.Size()) / (1024 * 1024)
			fmt.Fprintf(&left, "[::b]Transcript:[-]  %.1f MB\n", sizeMB)
		}
	}

	if qs, ok := d.statsCache[sess.Metadata.TranscriptPath]; ok {
		fmt.Fprintf(&left, "[::b]Tokens:[-]      ~%s\n", fmtTokens(qs.EstimatedTokens))
		fmt.Fprintf(&left, "[::b]Compactions:[-] %d\n", qs.Compactions)
		fmt.Fprintf(&left, "[::b]In context:[-]  %s entries\n", fmtNumber(qs.EntriesInContext))
		if qs.Compactions > 0 && !qs.LastCompactTime.IsZero() {
			fmt.Fprintf(&left, "[::b]Last compact:[-] %s\n", util.FormatRelativeTime(qs.LastCompactTime))
		}
		fmt.Fprintf(&left, "[::b]Total:[-]       %s entries\n", fmtNumber(qs.TotalEntries))
	} else if sess.Metadata.TranscriptPath != "" {
		left.WriteString("[gray]Computing stats...[-]\n")
	}

	d.leftCol.SetText(left.String())

	// RIGHT COLUMN: messages + technical
	var right strings.Builder

	if len(detail.Messages) > 0 {
		right.WriteString("[::b]Last exchange:[-]\n\n")
		for _, msg := range detail.Messages {
			role := "[green]You:[-]"
			if msg.Role == "assistant" {
				role = "[blue]Claude:[-]"
			}
			text := msg.Text
			if len(text) > 120 {
				text = text[:117] + "..."
			}
			text = tview.Escape(text)
			fmt.Fprintf(&right, "  %s %s\n", role, text)
		}
		right.WriteString("\n")
	}

	right.WriteString("[gray::d]")
	fmt.Fprintf(&right, "UUID: %s\n", sess.Metadata.SessionID)
	if len(sess.Metadata.PreviousSessionIDs) > 0 {
		fmt.Fprintf(&right, "Previous: %d session(s)\n", len(sess.Metadata.PreviousSessionIDs))
	}
	fmt.Fprintf(&right, "clotilde resume %s\n", sess.Name)
	fmt.Fprintf(&right, "claude --resume %s\n", sess.Metadata.SessionID)
	right.WriteString("[-:-:-]")

	d.rightCol.SetText(right.String())
}

// fmtTokens formats a token count as "97k" or "1.2M".
func fmtTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%dk", n/1_000)
	}
	return fmt.Sprintf("%d", n)
}

// fmtNumber formats a number with comma separators.
func fmtNumber(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
