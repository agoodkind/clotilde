package ui

import (
	"fmt"
	"os"
	"strings"

	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// SessionDetail holds pre-extracted data for the details pane.
// The caller extracts this from the claude package to avoid import cycles.
type SessionDetail struct {
	Model    string
	Messages []DetailMessage // last N non-tool messages
}

// DetailMessage is a simplified message for display.
type DetailMessage struct {
	Role string // "user" or "assistant"
	Text string
}

// DetailsPane shows rich session information at the bottom of the screen.
type DetailsPane struct {
	*tview.TextView
	sess       *session.Session
	statsCache map[string]*transcript.CompactQuickStats
}

// NewDetailsPane creates a scrollable details pane.
func NewDetailsPane() *DetailsPane {
	tv := tview.NewTextView().
		SetDynamicColors(true).
		SetScrollable(true).
		SetWordWrap(true)

	tv.SetBorder(true).
		SetTitle(" DETAILS ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(ColorInfo)

	return &DetailsPane{
		TextView:   tv,
		statsCache: make(map[string]*transcript.CompactQuickStats),
	}
}

// SetStatsCache shares the stats cache from the session table.
func (d *DetailsPane) SetStatsCache(cache map[string]*transcript.CompactQuickStats) {
	d.statsCache = cache
}

// ShowSession populates the details pane with session information.
// detail contains pre-extracted model and messages (to avoid import cycles with claude pkg).
func (d *DetailsPane) ShowSession(sess *session.Session, detail SessionDetail) {
	d.sess = sess
	d.Clear()
	d.ScrollToBeginning()

	if sess == nil {
		return
	}

	var b strings.Builder

	// Identity
	fmt.Fprintf(&b, "[::b]%s[-:-:-]\n", tview.Escape(sess.Name))
	if sess.Metadata.Context != "" {
		fmt.Fprintf(&b, "[gray]%s[-]\n", tview.Escape(sess.Metadata.Context))
	}
	b.WriteString("\n")

	// Core info
	ws := shortPath(sess.Metadata.WorkspaceRoot)
	fmt.Fprintf(&b, "[white::b]Model:[-:-:-]       %s\n", detail.Model)
	fmt.Fprintf(&b, "[white::b]Workspace:[-:-:-]   %s\n", ws)
	if sess.Metadata.IsForkedSession {
		fmt.Fprintf(&b, "[white::b]Type:[-:-:-]        [yellow]fork of %s[-]\n", sess.Metadata.ParentSession)
	}
	b.WriteString("\n")

	// Timing
	fmt.Fprintf(&b, "[white::b]Created:[-:-:-]     %s\n", sess.Metadata.Created.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "[white::b]Last used:[-:-:-]   %s\n", util.FormatRelativeTime(sess.Metadata.LastAccessed))
	b.WriteString("\n")

	// Transcript stats
	if sess.Metadata.TranscriptPath != "" {
		if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
			sizeMB := float64(info.Size()) / (1024 * 1024)
			fmt.Fprintf(&b, "[white::b]Transcript:[-:-:-]  %.1f MB\n", sizeMB)
		}
	}

	// Context window stats from cache
	if qs, ok := d.statsCache[sess.Metadata.TranscriptPath]; ok {
		fmt.Fprintf(&b, "[white::b]Tokens:[-:-:-]      ~%s (tiktoken estimate)\n", fmtTokens(qs.EstimatedTokens))
		fmt.Fprintf(&b, "[white::b]Compactions:[-:-:-] %d\n", qs.Compactions)
		fmt.Fprintf(&b, "[white::b]In context:[-:-:-]  %s entries\n", fmtNumber(qs.EntriesInContext))
		if qs.Compactions > 0 && !qs.LastCompactTime.IsZero() {
			fmt.Fprintf(&b, "[white::b]Last compact:[-:-:-] %s\n", util.FormatRelativeTime(qs.LastCompactTime))
		}
		fmt.Fprintf(&b, "[white::b]Total:[-:-:-]       %s entries\n", fmtNumber(qs.TotalEntries))
	} else if sess.Metadata.TranscriptPath != "" {
		b.WriteString("[gray]Computing context stats...[-]\n")
	}
	b.WriteString("\n")

	// Last 5 messages (pre-extracted by caller)
	if len(detail.Messages) > 0 {
		b.WriteString("[white::b]Last exchange:[-:-:-]\n")
		for _, msg := range detail.Messages {
			role := "[green]You:[-]"
			if msg.Role == "assistant" {
				role = "[blue]Claude:[-]"
			}
			text := msg.Text
			if len(text) > 100 {
				text = text[:97] + "..."
			}
			text = tview.Escape(text)
			fmt.Fprintf(&b, "  %s %s\n", role, text)
		}
		b.WriteString("\n")
	}

	// Technical
	fmt.Fprintf(&b, "[gray]UUID: %s[-]\n", sess.Metadata.SessionID)
	if len(sess.Metadata.PreviousSessionIDs) > 0 {
		fmt.Fprintf(&b, "[gray]Previous: %d session(s)[-]\n", len(sess.Metadata.PreviousSessionIDs))
	}
	fmt.Fprintf(&b, "[gray]clotilde resume %s[-]\n", sess.Name)
	fmt.Fprintf(&b, "[gray]claude --resume %s[-]\n", sess.Metadata.SessionID)

	d.SetText(b.String())
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
