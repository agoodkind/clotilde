package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fgrehm/clotilde/internal/session"
)

// PreviewFunc generates preview text for a session in the picker pane.
// If nil, the default built-in preview is used.
type PreviewFunc func(sess *session.Session) string

// PickerModel represents the session picker state
type PickerModel struct {
	Sessions    []*session.Session
	Cursor      int
	Selected    *session.Session
	Cancelled   bool
	Title       string
	FilterText  string
	Filtering   bool
	ShowPreview bool        // Show preview pane with session metadata
	PreviewFn   PreviewFunc // Custom preview renderer (optional)
	width       int         // terminal width (updated on resize)
	height      int         // terminal height (updated on resize)
}

// NewPicker creates a new session picker
func NewPicker(sessions []*session.Session, title string) PickerModel {
	return PickerModel{
		Sessions: sessions,
		Title:    title,
		Cursor:   0,
	}
}

// WithPreview enables the preview pane
func (m PickerModel) WithPreview() PickerModel {
	m.ShowPreview = true
	return m
}

// Init initializes the model (required by bubbletea)
func (m PickerModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input and window resize
func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		// Handle filter mode separately
		if m.Filtering {
			switch msg.String() {
			case "esc":
				// Exit filter mode, clear filter
				m.Filtering = false
				m.FilterText = ""
				m.Cursor = 0
				return m, nil

			case "enter":
				// Exit filter mode, keep filter
				m.Filtering = false
				return m, nil

			case "backspace":
				if len(m.FilterText) > 0 {
					m.FilterText = m.FilterText[:len(m.FilterText)-1]
					m.Cursor = 0 // Reset cursor when filter changes
				}
				return m, nil

			default:
				// Add character to filter
				if len(msg.Runes) == 1 {
					m.FilterText += string(msg.Runes[0])
					m.Cursor = 0 // Reset cursor when filter changes
				}
				return m, nil
			}
		}

		// Normal mode (not filtering)
		switch msg.String() {
		case "ctrl+c":
			m.Cancelled = true
			return m, tea.Quit

		case "q":
			if !m.Filtering {
				m.Cancelled = true
				return m, tea.Quit
			}

		case "esc":
			if m.FilterText != "" {
				// Clear existing filter
				m.FilterText = ""
				m.Cursor = 0
				return m, nil
			}
			m.Cancelled = true
			return m, tea.Quit

		case "/":
			// Enter filter mode
			m.Filtering = true
			return m, nil

		case "enter", " ":
			filtered := m.filteredSessions()
			if len(filtered) > 0 {
				m.Selected = filtered[m.Cursor]
			}
			return m, tea.Quit

		case "up", "k":
			if m.Cursor > 0 {
				m.Cursor--
			}
			return m, nil

		case "down", "j":
			filtered := m.filteredSessions()
			if m.Cursor < len(filtered)-1 {
				m.Cursor++
			}
			return m, nil

		case "home", "g":
			m.Cursor = 0
			return m, nil

		case "end", "G":
			filtered := m.filteredSessions()
			if len(filtered) > 0 {
				m.Cursor = len(filtered) - 1
			}
			return m, nil
		}
	}

	return m, nil
}

// View renders the session picker
func (m PickerModel) View() string {
	if m.ShowPreview {
		return m.viewWithPreview()
	}
	return m.viewSimple()
}

// viewSimple renders the picker without preview pane
func (m PickerModel) viewSimple() string {
	var b strings.Builder

	// Title
	titleStyle := BoldStyle
	b.WriteString(titleStyle.Render(m.Title))
	b.WriteString("\n\n")

	// Filter input (if active or has text)
	if m.Filtering || m.FilterText != "" {
		filterPrefix := "Filter: "
		if m.Filtering {
			filterPrefix = "Filter (type to search): "
		}
		filterStyle := InfoStyle
		b.WriteString(filterStyle.Render(filterPrefix))
		b.WriteString(m.FilterText)
		if m.Filtering {
			b.WriteString("█") // Cursor
		}
		b.WriteString("\n\n")
	}

	// Get filtered sessions
	filtered := m.filteredSessions()

	// No sessions
	if len(filtered) == 0 {
		emptyStyle := DimStyle.Italic(true)
		if m.FilterText != "" {
			b.WriteString(emptyStyle.Render(fmt.Sprintf("No sessions matching '%s'", m.FilterText)))
		} else {
			b.WriteString(emptyStyle.Render("No sessions available"))
		}
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("Press / to filter, q or Esc to cancel"))
		return b.String()
	}

	// Session list
	for i, sess := range filtered {
		cursor := " "
		if m.Cursor == i {
			cursor = ">"
		}

		// Build session line
		sessionLine := m.formatSessionLine(sess)

		// Highlight matching text
		if m.FilterText != "" {
			sessionLine = m.highlightMatch(sessionLine, m.FilterText)
		}

		// Highlight selected
		if m.Cursor == i {
			sessionLine = lipgloss.NewStyle().
				Foreground(SuccessColor).
				Bold(true).
				Render(sessionLine)
		}

		fmt.Fprintf(&b, "%s %s\n", cursor, sessionLine)
	}

	// Help text
	b.WriteString("\n")
	helpStyle := DimStyle.Italic(true)
	if m.FilterText != "" {
		b.WriteString(helpStyle.Render("(Esc to clear filter, / to edit, ↑/↓ to navigate, enter to select)"))
	} else {
		b.WriteString(helpStyle.Render("(/ to filter, ↑/↓ or j/k to navigate, enter to select, q to quit)"))
	}

	return b.String()
}

// viewWithPreview renders the picker with a preview pane (split view).
// Hides the preview pane when the terminal is too narrow (< 80 columns).
func (m PickerModel) viewWithPreview() string {
	filtered := m.filteredSessions()

	// Hide preview on narrow terminals
	if m.width > 0 && m.width < 80 {
		return m.viewSimple()
	}

	// Build list pane
	listPane := m.renderListPane(filtered)

	// Build preview pane with width cap
	var previewPane string
	if len(filtered) > 0 {
		previewContent := m.getPreviewContent(filtered[m.Cursor])
		// Cap preview width to half the terminal (or 50 chars minimum)
		previewWidth := 50
		if m.width > 0 {
			previewWidth = m.width/2 - 4
			if previewWidth < 40 {
				previewWidth = 40
			}
		}
		// Truncate lines to fit width and cap height
		lines := strings.Split(previewContent, "\n")
		maxLines := 20
		if m.height > 0 {
			maxLines = m.height - 6
		}
		if len(lines) > maxLines {
			lines = lines[:maxLines]
		}
		for i, line := range lines {
			if len(line) > previewWidth-4 {
				lines[i] = line[:previewWidth-7] + "..."
			}
		}
		previewPane = InfoBoxStyle.Width(previewWidth).Render(strings.Join(lines, "\n"))
	} else {
		previewPane = DimStyle.Italic(true).Render("No session selected")
	}

	// Join panes side by side
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
	titleStyle := BoldStyle
	b.WriteString(titleStyle.Render(m.Title))
	b.WriteString("\n\n")

	// Filter input (if active or has text)
	if m.Filtering || m.FilterText != "" {
		filterPrefix := "Filter: "
		if m.Filtering {
			filterPrefix = "Filter: "
		}
		filterStyle := InfoStyle
		b.WriteString(filterStyle.Render(filterPrefix))
		b.WriteString(m.FilterText)
		if m.Filtering {
			b.WriteString("█")
		}
		b.WriteString("\n\n")
	}

	// No sessions
	if len(filtered) == 0 {
		emptyStyle := DimStyle.Italic(true)
		if m.FilterText != "" {
			b.WriteString(emptyStyle.Render(fmt.Sprintf("No matches for '%s'", m.FilterText)))
		} else {
			b.WriteString(emptyStyle.Render("No sessions"))
		}
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("/ filter, q quit"))
		return b.String()
	}

	// Session list (limit to visible area)
	maxVisible := 10
	if m.height > 12 {
		maxVisible = m.height - 8 // leave room for title, filter, help
	}
	start := max(m.Cursor-maxVisible/2, 0)
	end := start + maxVisible
	if end > len(filtered) {
		end = len(filtered)
		start = max(end-maxVisible, 0)
	}

	for i := start; i < end; i++ {
		sess := filtered[i]
		cursor := " "
		if m.Cursor == i {
			cursor = ">"
		}

		// Build session line with "last used" info
		sessionLine := m.formatSessionLineWithTime(sess)

		// Highlight selected
		if m.Cursor == i {
			sessionLine = lipgloss.NewStyle().
				Foreground(SuccessColor).
				Bold(true).
				Render(sessionLine)
		}

		fmt.Fprintf(&b, "%s %s\n", cursor, sessionLine)
	}

	// Help text
	b.WriteString("\n")
	helpStyle := DimStyle.Italic(true)
	b.WriteString(helpStyle.Render("↑/↓ navigate · / filter · enter select · q quit"))

	return b.String()
}

// defaultPreview renders the built-in preview for a session (with box styling).
func defaultPreview(sess *session.Session) string {
	var lines []string

	// Session name header
	nameStyle := BoldStyle
	if sess.Metadata.IsForkedSession {
		nameStyle = lipgloss.NewStyle().Foreground(ForkColor).Bold(true)
	} else if sess.Metadata.IsIncognito {
		nameStyle = lipgloss.NewStyle().Foreground(IncognitoColor).Bold(true)
	}
	lines = append(lines, nameStyle.Render(sess.Name))
	lines = append(lines, "")

	// Session type
	if sess.Metadata.IsForkedSession {
		lines = append(lines, DimStyle.Render("Type:")+"  "+lipgloss.NewStyle().Foreground(ForkColor).Render("Fork of "+sess.Metadata.ParentSession))
	} else if sess.Metadata.IsIncognito {
		lines = append(lines, DimStyle.Render("Type:")+"  "+lipgloss.NewStyle().Foreground(IncognitoColor).Render("Incognito"))
	}

	// Timestamps
	lines = append(lines, DimStyle.Render("Created:")+"     "+sess.Metadata.Created.Format("2006-01-02 15:04"))
	lines = append(lines, DimStyle.Render("Last used:")+"   "+formatTimeAgo(sess.Metadata.LastAccessed))

	return strings.Join(lines, "\n")
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

// filteredSessions returns sessions that match the current filter
func (m PickerModel) filteredSessions() []*session.Session {
	if m.FilterText == "" {
		return m.Sessions
	}

	var filtered []*session.Session
	lowerFilter := strings.ToLower(m.FilterText)

	for _, sess := range m.Sessions {
		if strings.Contains(strings.ToLower(sess.Name), lowerFilter) {
			filtered = append(filtered, sess)
		}
	}

	return filtered
}

// highlightMatch highlights the matching part of the text (simple version)
func (m PickerModel) highlightMatch(text, filter string) string {
	// For now, just return the text as-is
	// A full implementation would highlight the matching substring
	return text
}

// formatSessionLineWithTime formats a session line with "last used" time
func (m PickerModel) formatSessionLineWithTime(sess *session.Session) string {
	name := sess.Name

	// Add type indicator
	if sess.Metadata.IsForkedSession {
		typeStyle := lipgloss.NewStyle().Foreground(ForkColor)
		name += typeStyle.Render(" [fork]")
	} else if sess.Metadata.IsIncognito {
		typeStyle := lipgloss.NewStyle().Foreground(IncognitoColor)
		name += typeStyle.Render(" [inc]")
	}

	// Add time ago
	timeAgo := DimStyle.Render(" · " + formatTimeAgo(sess.Metadata.LastAccessed))

	return name + timeAgo
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
	p := tea.NewProgram(model, tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run picker: %w", err)
	}

	finalModel := m.(PickerModel)
	if finalModel.Cancelled {
		return nil, nil
	}

	return finalModel.Selected, nil
}
