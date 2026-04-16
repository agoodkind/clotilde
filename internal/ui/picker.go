package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	bl "github.com/winder/bubblelayout"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// PreviewFunc generates preview text for a session in the picker pane.
// If nil, the default built-in preview is used.
type PreviewFunc func(sess *session.Session) string

// SortMode controls how sessions are sorted in the picker.
type SortMode int

const (
	SortByLastUsed SortMode = iota
	SortByCreated
	SortByName
)

// sessionStatsMsg is delivered when a background stat computation completes.
type sessionStatsMsg struct {
	Path  string
	Stats transcript.CompactQuickStats
}

// PickerModel represents the session picker state
type PickerModel struct {
	Sessions    []*session.Session
	Selected    *session.Session
	Cancelled   bool
	Title       string
	ShowPreview bool        // Show preview pane with session metadata
	PreviewFn   PreviewFunc // Custom preview renderer (optional)
	Nav         ListNav
	Filter      FilterState
	Term        TermSize
	Sort        SortMode // current sort (default: last used)

	// Stats cache: transcript path → computed/cached stats.
	// The PreviewFn reads this map so it can show stats instantly on cache hits
	// and "Computing…" while background goroutines work on misses.
	StatsCache map[string]*transcript.CompactQuickStats

	// Preview viewport for scrollable preview pane
	previewVP      viewport.Model
	previewReady   bool
	previewFocused bool // true when tab switches focus to preview
	lastPreviewIdx int  // track cursor changes to refresh viewport content

	// BubbleLayout for responsive list+preview
	layout      bl.BubbleLayout
	listID      bl.ID
	previewID   bl.ID
	listWidth   int
	listHeight  int
	prevWidth   int
	prevHeight  int
	layoutReady bool
}

// NewPicker creates a new session picker
func NewPicker(sessions []*session.Session, title string) PickerModel {
	m := PickerModel{
		Sessions:   sessions,
		Title:      title,
		StatsCache: make(map[string]*transcript.CompactQuickStats),
	}
	return m
}

// WithPreview enables the preview pane and initializes the BubbleLayout
func (m PickerModel) WithPreview() PickerModel {
	m.ShowPreview = true
	m.layout = bl.New()
	m.listID = m.layout.Add("w 30, grow")
	m.previewID = m.layout.Add("w 25, grow")
	return m
}

// Init initializes the model (required by bubbletea)
func (m PickerModel) Init() tea.Cmd {

	// Pre-warm stats cache. Sessions with a fresh disk cache entry are loaded
	// immediately. Stale or missing entries are computed in background goroutines
	// (max 3 concurrent) and sent back as sessionStatsMsg so the preview pane
	// re-renders when they arrive.
	var cmds []tea.Cmd
	sem := make(chan struct{}, 3) // semaphore limits concurrent computations to 3

	for _, sess := range m.Sessions {
		path := sess.Metadata.TranscriptPath
		if path == "" {
			continue
		}

		// Disk cache hit: populate in-memory cache immediately, no goroutine needed.
		if cached := transcript.LoadCachedStats(path); cached != nil {
			statsCopy := cached.Stats
			m.StatsCache[path] = &statsCopy
			continue
		}

		// Cache miss: schedule background computation.
		p := path // capture loop variable
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()

			qs, err := transcript.QuickStats(p)
			if err != nil {
				return nil //nolint:nilnil // background stat failure is intentionally silent
			}

			// Persist to disk so the next picker open is instant.
			if info, statErr := os.Stat(p); statErr == nil {
				transcript.SaveCachedStats(p, qs, info.ModTime())
			}

			return sessionStatsMsg{Path: p, Stats: qs}
		})
	}

	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

// Update handles keyboard input and window resize
func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case sessionStatsMsg:
		// Store the result and invalidate the preview viewport so the
		// next render picks up the new stats.
		stats := msg.Stats
		m.StatsCache[msg.Path] = &stats
		m.lastPreviewIdx = -1 // force preview content refresh
		return m, nil

	case tea.WindowSizeMsg:
		m.Term.HandleResize(msg)
		if m.ShowPreview && m.layout != nil {
			layoutMsg := m.layout.Resize(msg.Width, msg.Height-2) // -2 for help bar
			if sz, err := layoutMsg.Size(m.listID); err == nil {
				m.listWidth = sz.Width
				m.listHeight = sz.Height
			}
			if sz, err := layoutMsg.Size(m.previewID); err == nil {
				m.prevWidth = sz.Width - 2 // room for scrollbar
				m.prevHeight = sz.Height
				if !m.previewReady {
					m.previewVP = viewport.New(m.prevWidth, m.prevHeight)
					m.previewReady = true
				} else {
					m.previewVP.Width = m.prevWidth
					m.previewVP.Height = m.prevHeight
				}
			}
			m.layoutReady = true
		}
		return m, nil

	case tea.MouseMsg:
		return m.handleMouse(msg)

	case tea.KeyMsg:
		key := msg.String()

		// Handle filter mode separately
		if m.Filter.Active {
			if m.Filter.HandleFilterKey(key, msg.Runes) {
				m.Nav.Cursor = 0
			}
			return m, nil
		}

		// Sort keys (only when not filtering)
		switch key {
		case "1":
			m.Sort = SortByName
			m.Nav.Cursor = 0
			return m, nil
		case "2":
			m.Sort = SortByCreated
			m.Nav.Cursor = 0
			return m, nil
		case "3":
			m.Sort = SortByLastUsed
			m.Nav.Cursor = 0
			return m, nil
		}

		// Tab toggles focus between list and preview
		if key == "tab" && m.ShowPreview && m.previewReady {
			m.previewFocused = !m.previewFocused
			return m, nil
		}

		// When preview is focused, forward scroll keys to viewport
		if m.previewFocused && m.previewReady {
			switch key {
			case "up", "k":
				m.previewVP.LineUp(1)
				return m, nil
			case "down", "j":
				m.previewVP.LineDown(1)
				return m, nil
			case "pgup":
				m.previewVP.HalfViewUp()
				return m, nil
			case "pgdown":
				m.previewVP.HalfViewDown()
				return m, nil
			case "enter", " ":
				// Enter still selects even from preview focus
				filtered := m.sortedFilteredSessions()
				if len(filtered) > 0 {
					m.Selected = filtered[m.Nav.Cursor]
				}
				return m, tea.Quit
			}
			// Quit keys still work from preview
			if quit, clearFilter := HandleQuitKeys(key, m.Filter.Active, m.Filter.Text); quit {
				m.Cancelled = true
				return m, tea.Quit
			} else if clearFilter {
				m.Filter.Text = ""
				m.Nav.Cursor = 0
				return m, nil
			}
			return m, nil
		}

		// Quit keys (list focused)
		if quit, clearFilter := HandleQuitKeys(key, m.Filter.Active, m.Filter.Text); quit {
			m.Cancelled = true
			return m, tea.Quit
		} else if clearFilter {
			m.Filter.Text = ""
			m.Nav.Cursor = 0
			return m, nil
		}

		switch key {
		case "/":
			m.Filter.Active = true
			return m, nil

		case "enter", " ":
			filtered := m.sortedFilteredSessions()
			if len(filtered) > 0 {
				m.Selected = filtered[m.Nav.Cursor]
			}
			return m, tea.Quit
		}

		// Navigation
		m.Nav.Total = len(m.sortedFilteredSessions())
		if m.Nav.HandleKey(key) {
			return m, nil
		}
	}

	return m, nil
}

// handleMouse processes mouse events for the picker
func (m PickerModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	filtered := m.sortedFilteredSessions()
	if len(filtered) == 0 {
		return m, nil
	}

	switch msg.Type {
	case tea.MouseLeft:
		// Determine which session line was clicked
		clickedIdx := m.mouseYToIndex(msg.Y, len(filtered))
		if clickedIdx >= 0 {
			m.Nav.Cursor = clickedIdx
		}
		return m, nil

	case tea.MouseWheelUp:
		if m.Nav.Cursor > 0 {
			m.Nav.Cursor--
		}
		return m, nil

	case tea.MouseWheelDown:
		if m.Nav.Cursor < len(filtered)-1 {
			m.Nav.Cursor++
		}
		return m, nil
	}

	return m, nil
}

// mouseYToIndex maps a mouse Y coordinate to a session index in the filtered list.
// Returns -1 if the click is outside the visible session lines.
func (m PickerModel) mouseYToIndex(y int, totalFiltered int) int {
	// Compute the Y offset where session lines start:
	// Title line + blank line = 2
	yOffset := 2
	// Filter input adds 2 lines (text + blank) when active or has text
	if m.Filter.Active || m.Filter.Text != "" {
		yOffset += 2
	}

	// Compute visible window (same logic as renderListPane)
	maxVisible := m.Term.VisibleLines(8)
	start := max(m.Nav.Cursor-maxVisible/2, 0)
	end := start + maxVisible
	if end > totalFiltered {
		end = totalFiltered
		start = max(end-maxVisible, 0)
	}

	relY := y - yOffset
	if relY < 0 {
		return -1
	}

	idx := start + relY
	if idx < 0 || idx >= totalFiltered {
		return -1
	}
	return idx
}

// View renders the session picker
func (m PickerModel) View() string {
	if m.ShowPreview && m.layoutReady {
		return m.viewWithLayout()
	}
	if m.ShowPreview {
		return m.viewWithPreview()
	}
	return m.viewSimple()
}

// viewWithLayout renders using BubbleLayout-allocated sizes
func (m PickerModel) viewWithLayout() string {
	filtered := m.sortedFilteredSessions()

	// Render list pane constrained to layout width
	listPane := m.renderListPaneConstrained(filtered, m.listWidth, m.listHeight)

	// If preview has no space, skip it
	if m.prevWidth < 20 || len(filtered) == 0 {
		help := RenderHelpBar("↑↓ navigate · 1/2/3 sort · / filter · enter select · q quit")
		return lipgloss.JoinVertical(lipgloss.Left, listPane, "", help)
	}

	// Update preview viewport content
	if m.previewReady {
		previewContent := m.getPreviewContent(filtered[m.Nav.Cursor])
		if m.Nav.Cursor != m.lastPreviewIdx || m.previewVP.TotalLineCount() == 0 {
			m.previewVP.SetContent(previewContent)
			m.previewVP.GotoTop()
			m.lastPreviewIdx = m.Nav.Cursor
		}
	}

	// Preview with scrollbar
	var previewPane string
	if m.previewReady {
		borderStyle := InfoBoxStyle.Width(m.prevWidth + 2)
		if m.previewFocused {
			borderStyle = borderStyle.BorderForeground(SuccessColor)
		}
		previewPane = borderStyle.Render(ViewportWithScrollbar(m.previewVP))
	}

	content := lipgloss.JoinHorizontal(lipgloss.Top, listPane, " ", previewPane)

	// Help bar
	var help string
	if m.previewFocused {
		help = RenderHelpBar("↑↓ scroll · tab list · enter select · q quit")
	} else {
		help = RenderHelpBar("↑↓ navigate · 1/2/3 sort · tab preview · / filter · enter select · q quit")
	}

	return lipgloss.JoinVertical(lipgloss.Left, content, help)
}

// renderListPaneConstrained renders the list pane within given width/height constraints
func (m PickerModel) renderListPaneConstrained(filtered []*session.Session, width, height int) string {
	var b strings.Builder

	b.WriteString(BoldStyle.Render(m.Title))
	b.WriteString("\n\n")
	b.WriteString(m.Filter.RenderFilterInput())

	if len(filtered) == 0 {
		b.WriteString(RenderEmptyState(m.Filter.Text, "sessions"))
		return b.String()
	}

	// Calculate visible lines from layout height
	maxVisible := height - 5 // title(1) + blank(1) + filter(2) + help(1)
	if maxVisible < 3 {
		maxVisible = 3
	}

	start := max(m.Nav.Cursor-maxVisible/2, 0)
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
		start = max(end-maxVisible, 0)
	}

	for i := start; i < end; i++ {
		sess := filtered[i]
		line := m.formatSessionLineConstrained(sess, width-4) // -4 for cursor+padding
		b.WriteString(RenderCursorLine(i, m.Nav.Cursor, line))
		b.WriteString("\n")
	}

	return b.String()
}

// formatSessionLineConstrained formats a session line within a max width
func (m PickerModel) formatSessionLineConstrained(sess *session.Session, maxWidth int) string {
	name := sess.Name
	if len(name) > maxWidth-15 { // leave room for time
		name = name[:maxWidth-18] + "..."
	}

	timeAgo := formatTimeAgo(sess.Metadata.LastAccessed)
	padding := maxWidth - len(name) - len(timeAgo)
	if padding < 2 {
		padding = 2
	}

	return name + strings.Repeat(" ", padding) + DimStyle.Render(timeAgo)
}

// viewSimple renders the picker without preview pane
func (m PickerModel) viewSimple() string {
	var b strings.Builder

	// Title
	b.WriteString(BoldStyle.Render(m.Title))
	b.WriteString("\n\n")

	// Filter input
	b.WriteString(m.Filter.RenderFilterInput())

	// Get filtered+sorted sessions
	filtered := m.sortedFilteredSessions()

	// No sessions
	if len(filtered) == 0 {
		b.WriteString(RenderEmptyState(m.Filter.Text, "sessions"))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("Press / to filter, q or Esc to cancel"))
		return b.String()
	}

	// Session list
	for i, sess := range filtered {
		sessionLine := m.formatSessionLine(sess)
		b.WriteString(RenderCursorLine(i, m.Nav.Cursor, sessionLine))
		b.WriteString("\n")
	}

	// Help text
	b.WriteString("\n")
	if m.Filter.Text != "" {
		b.WriteString(RenderHelpBar("Esc clear filter · ↑↓ navigate · enter select"))
	} else {
		b.WriteString(RenderHelpBar("↑↓ navigate · 1/2/3 sort · / filter · enter select · q quit"))
	}

	return b.String()
}

// viewWithPreview renders the picker with a preview pane.
// Layout adapts to terminal width:
//   - Wide (>= 100): side-by-side (list | preview)
//   - Medium (60-99): stacked vertically (list above, preview below)
//   - Narrow (< 60): list only, no preview
func (m PickerModel) viewWithPreview() string {
	filtered := m.sortedFilteredSessions()

	// Narrow: list only
	if m.Term.Width > 0 && m.Term.Width < 60 {
		return m.viewSimple()
	}

	listPane := m.renderListPane(filtered)

	// No preview content if nothing selected
	if len(filtered) == 0 {
		return listPane
	}

	previewContent := m.getPreviewContent(filtered[m.Nav.Cursor])

	// Medium: stack vertically
	if m.Term.Width > 0 && m.Term.Width < 100 {
		previewLines := strings.Split(previewContent, "\n")
		maxPreviewLines := 8
		if m.Term.Height > 20 {
			maxPreviewLines = m.Term.Height / 3
		}
		if len(previewLines) > maxPreviewLines {
			previewLines = previewLines[:maxPreviewLines]
		}
		previewWidth := m.Term.Width - 4
		if previewWidth < 30 {
			previewWidth = 30
		}
		for i, line := range previewLines {
			if len(line) > previewWidth-4 {
				previewLines[i] = line[:previewWidth-7] + "..."
			}
		}
		preview := InfoBoxStyle.Width(previewWidth).Render(strings.Join(previewLines, "\n"))
		return lipgloss.JoinVertical(lipgloss.Left, listPane, "", preview)
	}

	// Wide: side-by-side with scrollable preview viewport
	previewWidth := m.Term.Width/2 - 4
	if previewWidth < 40 {
		previewWidth = 40
	}

	// Update viewport content when cursor changes
	if m.previewReady {
		m.previewVP.Width = previewWidth - 4 // account for border
		if m.Nav.Cursor != m.lastPreviewIdx {
			m.previewVP.SetContent(previewContent)
			m.previewVP.GotoTop()
			m.lastPreviewIdx = m.Nav.Cursor
		} else if m.previewVP.TotalLineCount() == 0 {
			m.previewVP.SetContent(previewContent)
		}
	}

	// Render preview with border, highlight if focused
	var previewPane string
	if m.previewReady {
		borderStyle := InfoBoxStyle.Width(previewWidth)
		if m.previewFocused {
			borderStyle = borderStyle.BorderForeground(SuccessColor)
		}

		// Scroll indicator
		scrollInfo := ""
		if m.previewVP.TotalLineCount() > m.previewVP.Height {
			pct := m.previewVP.ScrollPercent() * 100
			scrollInfo = fmt.Sprintf(" %.0f%%", pct)
		}

		header := ""
		if m.previewFocused {
			header = DimStyle.Render("Preview (scrollable)") + scrollInfo + "\n"
		}

		previewPane = borderStyle.Render(header + m.previewVP.View())
	} else {
		previewPane = InfoBoxStyle.Width(previewWidth).Render(previewContent)
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		listPane,
		"  ",
		previewPane,
	)
}

// getPreviewContent returns the raw preview text (without box styling)
func (m PickerModel) getPreviewContent(sess *session.Session) string {
	if m.PreviewFn != nil {
		return m.PreviewFn(sess)
	}
	return defaultPreviewText(sess)
}

// defaultPreviewText returns the default preview text for a session
func defaultPreviewText(sess *session.Session) string {
	var lines []string
	lines = append(lines, sess.Name)
	lines = append(lines, "")
	if sess.Metadata.IsForkedSession {
		lines = append(lines, "Type:      Fork of "+sess.Metadata.ParentSession)
	}
	lines = append(lines, "Created:   "+sess.Metadata.Created.Format("2006-01-02 15:04"))
	lines = append(lines, "Last used: "+formatTimeAgo(sess.Metadata.LastAccessed))
	if sess.Metadata.Context != "" {
		lines = append(lines, "")
		lines = append(lines, sess.Metadata.Context)
	}
	return strings.Join(lines, "\n")
}

// renderListPane renders the left pane with session list
func (m PickerModel) renderListPane(filtered []*session.Session) string {
	var b strings.Builder

	// Title
	b.WriteString(BoldStyle.Render(m.Title))
	b.WriteString("\n\n")

	// Filter input
	b.WriteString(m.Filter.RenderFilterInput())

	// No sessions
	if len(filtered) == 0 {
		b.WriteString(RenderEmptyState(m.Filter.Text, "sessions"))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("/ filter, q quit"))
		return b.String()
	}

	// Session list (limit to visible area)
	maxVisible := m.Term.VisibleLines(8)

	start := max(m.Nav.Cursor-maxVisible/2, 0)
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
		start = max(end-maxVisible, 0)
	}

	for i := start; i < end; i++ {
		sess := filtered[i]
		sessionLine := m.formatSessionLineWithTime(sess)
		b.WriteString(RenderCursorLine(i, m.Nav.Cursor, sessionLine))
		b.WriteString("\n")
	}

	// Help text
	b.WriteString("\n")
	if m.previewFocused {
		b.WriteString(RenderHelpBar("↑↓ scroll · tab list · enter select · q quit"))
	} else {
		b.WriteString(RenderHelpBar("↑↓ navigate · 1/2/3 sort · tab preview · / filter · enter select · q quit"))
	}

	return b.String()
}

// formatSessionLine formats a single session for display
func (m PickerModel) formatSessionLine(sess *session.Session) string {
	name := sess.Name

	// Add type indicator
	typeIndicator := ""
	if sess.Metadata.IsForkedSession {
		typeStyle := lipgloss.NewStyle().Foreground(ForkColor)
		typeIndicator = typeStyle.Render(" [fork]")
	} else if sess.Metadata.IsIncognito {
		typeStyle := lipgloss.NewStyle().Foreground(IncognitoColor)
		typeIndicator = typeStyle.Render(" [incognito]")
	}

	return name + typeIndicator
}

// sortedFilteredSessions returns sessions that match the current filter, sorted by current sort mode.
func (m PickerModel) sortedFilteredSessions() []*session.Session {
	filtered := m.filteredSessions()
	m.sortSessions(filtered)
	return filtered
}

// filteredSessions returns sessions that match the current filter
func (m PickerModel) filteredSessions() []*session.Session {
	if m.Filter.Text == "" {
		// Return a copy so sorting doesn't mutate the original
		result := make([]*session.Session, len(m.Sessions))
		copy(result, m.Sessions)
		return result
	}

	var filtered []*session.Session
	lowerFilter := strings.ToLower(m.Filter.Text)

	for _, sess := range m.Sessions {
		if strings.Contains(strings.ToLower(sess.Name), lowerFilter) {
			filtered = append(filtered, sess)
		}
	}

	return filtered
}

// sortSessions sorts the given session slice in place according to the current sort mode.
func (m PickerModel) sortSessions(sessions []*session.Session) {
	switch m.Sort {
	case SortByName:
		sort.Slice(sessions, func(i, j int) bool {
			return strings.ToLower(sessions[i].Name) < strings.ToLower(sessions[j].Name)
		})
	case SortByCreated:
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].Metadata.Created.After(sessions[j].Metadata.Created)
		})
	case SortByLastUsed:
		sort.Slice(sessions, func(i, j int) bool {
			return sessions[i].Metadata.LastAccessed.After(sessions[j].Metadata.LastAccessed)
		})
	}
}

// formatSessionLineWithTime formats a session line with aligned columns:
// NAME (left, max 35) | DIR (left, max 20) | LAST USED (right, max 15)
func (m PickerModel) formatSessionLineWithTime(sess *session.Session) string {
	// Name column (max 35 chars)
	name := sess.Name
	if len(name) > 35 {
		name = name[:32] + "..."
	}

	// Add type indicator suffix (counted separately from truncation)
	if sess.Metadata.IsForkedSession {
		typeStyle := lipgloss.NewStyle().Foreground(ForkColor)
		name += typeStyle.Render(" [fork]")
	} else if sess.Metadata.IsIncognito {
		typeStyle := lipgloss.NewStyle().Foreground(IncognitoColor)
		name += typeStyle.Render(" [inc]")
	}

	// Dir column (max 20 chars)
	dir := pickerShortPath(sess.Metadata.WorkspaceRoot)
	if len(dir) > 20 {
		dir = dir[len(dir)-17:]
		dir = "..." + dir
	}

	// Time column
	timeAgo := formatTimeAgo(sess.Metadata.LastAccessed)

	// Pad the plain name for alignment (use raw name length, not styled)
	plainName := sess.Name
	if len(plainName) > 35 {
		plainName = plainName[:32] + "..."
	}
	// Calculate padding needed after styled name
	namePad := 35 - len(plainName)
	if namePad < 0 {
		namePad = 0
	}

	dirStyle := DimStyle
	timeStyle := DimStyle

	return fmt.Sprintf("%s%s  %s  %s",
		name,
		strings.Repeat(" ", namePad),
		dirStyle.Render(fmt.Sprintf("%-20s", dir)),
		timeStyle.Render(timeAgo),
	)
}

// pickerShortPath abbreviates a workspace root path for display.
func pickerShortPath(root string) string {
	if root == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Base(root)
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}

// formatTimeAgo formats a time as "X ago" (e.g., "2 hours ago", "just now")
func formatTimeAgo(t time.Time) string {
	duration := time.Since(t)

	switch {
	case duration.Seconds() < 60:
		return "just now"
	case duration.Minutes() < 2:
		return "1 minute ago"
	case duration.Minutes() < 60:
		return fmt.Sprintf("%d minutes ago", int(duration.Minutes()))
	case duration.Hours() < 2:
		return "1 hour ago"
	case duration.Hours() < 24:
		return fmt.Sprintf("%d hours ago", int(duration.Hours()))
	case duration.Hours() < 48:
		return "1 day ago"
	default:
		return fmt.Sprintf("%d days ago", int(duration.Hours()/24))
	}
}

// RunPicker runs the session picker and returns the selected session
func RunPicker(model PickerModel) (*session.Session, error) {
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run picker: %w", err)
	}

	finalModel, _ := m.(PickerModel)
	if finalModel.Cancelled {
		return nil, nil
	}

	return finalModel.Selected, nil
}
