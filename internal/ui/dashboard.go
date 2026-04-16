package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fgrehm/clotilde/internal/session"
)

// DashboardModel represents the main dashboard state
type DashboardModel struct {
	Sessions    []*session.Session
	Cursor      int
	Selected    string // Selected action ID
	Cancelled   bool
	Term        TermSize
	recentLimit int // How many recent sessions to show
	menuItems   []MenuItem
	vp          viewport.Model
	vpReady     bool
}

// MenuItem represents a menu action
type MenuItem struct {
	ID          string
	Label       string
	Description string
}

// NewDashboard creates a new dashboard model
func NewDashboard(sessions []*session.Session) DashboardModel {
	return newDashboard(sessions, nil)
}

// NewDashboardPostSession creates a dashboard with "Return to session" and "Exit"
// at the top, shown after a session exits.
func NewDashboardPostSession(sessions []*session.Session, lastSession *session.Session) DashboardModel {
	return newDashboard(sessions, lastSession)
}

func newDashboard(sessions []*session.Session, lastSession *session.Session) DashboardModel {
	var items []MenuItem

	if lastSession != nil {
		items = append(items,
			MenuItem{ID: "return", Label: "Return to " + lastSession.Name, Description: "Resume the session you just left"},
			MenuItem{ID: "quit", Label: "Quit", Description: "Exit clotilde"},
			MenuItem{ID: "", Label: "", Description: ""}, // separator
		)
	}

	items = append(items,
		MenuItem{ID: "start", Label: "Start new session", Description: "Create a new conversation"},
		MenuItem{ID: "resume", Label: "Browse sessions", Description: "Browse and resume an existing session"},
		MenuItem{ID: "view", Label: "View conversation", Description: "Read a session's conversation text"},
		MenuItem{ID: "search", Label: "Search conversation", Description: "Find where something was discussed (quick + depth options)"},
		MenuItem{ID: "fork", Label: "Fork session", Description: "Branch from an existing session"},
		MenuItem{ID: "auto-name", Label: "Auto-name sessions", Description: "Generate human-readable display names via LLM"},
		MenuItem{ID: "delete", Label: "Delete session", Description: "Remove a session"},
	)

	if lastSession == nil {
		items = append(items, MenuItem{ID: "quit", Label: "Quit", Description: "Exit clotilde"})
	}

	return DashboardModel{
		Sessions:    sessions,
		Cursor:      0,
		recentLimit: 5,
		menuItems:   items,
	}
}

// Init initializes the model (required by bubbletea)
func (m DashboardModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input
func (m DashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.Term.HandleResize(msg)
		if !m.vpReady {
			m.vp = viewport.New(msg.Width-2, msg.Height) // -2 for scrollbar + gap
			m.vpReady = true
		} else {
			m.vp.Width = msg.Width - 2 // -2 for scrollbar + gap
			m.vp.Height = msg.Height
		}
		m.vp.SetContent(m.renderContent())
		return m, nil

	case tea.MouseMsg:
		switch msg.Type {
		case tea.MouseWheelUp:
			if m.vpReady {
				m.vp.LineUp(3)
			}
			return m, nil
		case tea.MouseWheelDown:
			if m.vpReady {
				m.vp.LineDown(3)
			}
			return m, nil
		case tea.MouseLeft:
			for i, item := range m.menuItems {
				if item.ID != "" {
					m.Cursor = i
					m.vp.SetContent(m.renderContent())
					return m, nil
				}
			}
			return m, nil
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.Cancelled = true
			return m, tea.Quit

		case "enter", " ":
			if m.Cursor < len(m.menuItems) && m.menuItems[m.Cursor].ID != "" {
				m.Selected = m.menuItems[m.Cursor].ID
				return m, tea.Quit
			}
			return m, nil

		case "up", "k":
			for next := m.Cursor - 1; next >= 0; next-- {
				if m.menuItems[next].ID != "" {
					m.Cursor = next
					break
				}
			}
			return m, nil

		case "down", "j":
			for next := m.Cursor + 1; next < len(m.menuItems); next++ {
				if m.menuItems[next].ID != "" {
					m.Cursor = next
					break
				}
			}
			return m, nil

		case "home", "g":
			m.Cursor = 0
			return m, nil

		case "end", "G":
			m.Cursor = len(m.menuItems) - 1
			return m, nil
		}
	}

	return m, nil
}

// View renders the dashboard inside a scrollable viewport.
func (m DashboardModel) View() string {
	if m.vpReady {
		m.vp.SetContent(m.renderContent())
		return ViewportWithScrollbar(m.vp)
	}
	return m.renderContent()
}

// renderContent builds the full dashboard content string.
func (m DashboardModel) renderContent() string {
	var b strings.Builder

	b.WriteString(m.renderHeader())
	b.WriteString("\n\n")
	b.WriteString(m.renderMenu())
	b.WriteString("\n\n")
	b.WriteString(m.renderRecentSessions())
	b.WriteString("\n\n")
	b.WriteString(RenderHelpBar("↑↓ navigate · scroll viewport · enter select · q quit"))

	return b.String()
}

// renderHeader renders the styled header bar with name and session count
func (m DashboardModel) renderHeader() string {
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#00D7D7")). // Cyan
		Padding(0, 1)

	countStyle := lipgloss.NewStyle().
		Foreground(InfoColor).
		Bold(true)

	total := len(m.Sessions)
	forks := 0
	incognito := 0
	for _, sess := range m.Sessions {
		if sess.Metadata.IsForkedSession {
			forks++
		}
		if sess.Metadata.IsIncognito {
			incognito++
		}
	}

	var stats []string
	stats = append(stats, countStyle.Render(fmt.Sprintf("%d sessions", total)))
	if forks > 0 {
		forkStyle := lipgloss.NewStyle().Foreground(ForkColor)
		stats = append(stats, forkStyle.Render(fmt.Sprintf("%d forks", forks)))
	}
	if incognito > 0 {
		incognitoStyle := lipgloss.NewStyle().Foreground(IncognitoColor)
		stats = append(stats, incognitoStyle.Render(fmt.Sprintf("%d incognito", incognito)))
	}

	title := headerStyle.Render("clotilde")
	separator := DimStyle.Render(" | ")
	return title + separator + strings.Join(stats, DimStyle.Render(" · "))
}

// renderMenu renders the quick actions menu inside a bordered box
func (m DashboardModel) renderMenu() string {
	var lines []string

	for i, item := range m.menuItems {
		// Separator: render as blank line
		if item.ID == "" {
			lines = append(lines, "")
			continue
		}

		line := fmt.Sprintf("%s  %s", item.Label, DimStyle.Render("- "+item.Description))
		lines = append(lines, RenderCursorLine(i, m.Cursor, line))
	}

	content := strings.Join(lines, "\n")

	boxStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(MutedColor).
		Padding(0, 1)

	if m.Term.Width > 0 {
		boxStyle = boxStyle.Width(m.Term.Width - 4)
	}

	return boxStyle.Render(content)
}

// renderRecentSessions renders with the default limit.
func (m DashboardModel) renderRecentSessions() string {
	return m.renderRecentSessionsN(m.recentLimit)
}

// renderRecentSessionsN renders the recent sessions as a mini table with up to n entries.
func (m DashboardModel) renderRecentSessionsN(n int) string {
	if len(m.Sessions) == 0 {
		return DimStyle.Italic(true).Render("No sessions yet. Start one to get going!")
	}

	headerStyle := BoldStyle
	var b strings.Builder
	b.WriteString(headerStyle.Render("Recent Sessions"))
	b.WriteString("\n\n")

	limit := min(len(m.Sessions), n)

	narrow := m.Term.Width > 0 && m.Term.Width < 60

	// Column header
	dimBold := lipgloss.NewStyle().Foreground(MutedColor).Bold(true)
	if narrow {
		b.WriteString(fmt.Sprintf("  %s  %s\n", dimBold.Render(fmt.Sprintf("%-30s", "NAME")), dimBold.Render("LAST USED")))
	} else {
		b.WriteString(fmt.Sprintf("  %s  %s  %s\n", dimBold.Render(fmt.Sprintf("%-30s", "NAME")), dimBold.Render(fmt.Sprintf("%-20s", "WORKSPACE")), dimBold.Render("LAST USED")))
	}

	for i := range limit {
		sess := m.Sessions[i]

		name := sess.Name
		// Type indicator suffix
		if sess.Metadata.IsForkedSession {
			typeStyle := lipgloss.NewStyle().Foreground(ForkColor)
			name += typeStyle.Render(" [fork]")
		} else if sess.Metadata.IsIncognito {
			typeStyle := lipgloss.NewStyle().Foreground(IncognitoColor)
			name += typeStyle.Render(" [inc]")
		}

		// Truncate name for alignment
		displayName := sess.Name
		if len(displayName) > 28 {
			displayName = displayName[:25] + "..."
		}

		timeAgo := formatTimeAgo(sess.Metadata.LastAccessed)
		workspace := dashboardShortPath(sess.Metadata.WorkspaceRoot)

		dimLine := lipgloss.NewStyle().Foreground(MutedColor)
		if narrow {
			b.WriteString(fmt.Sprintf("  %s  %s\n", fmt.Sprintf("%-30s", displayName), dimLine.Render(timeAgo)))
		} else {
			ws := workspace
			if len(ws) > 20 {
				ws = ws[len(ws)-17:]
				ws = "..." + ws
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n", fmt.Sprintf("%-30s", displayName), dimLine.Render(fmt.Sprintf("%-20s", ws)), dimLine.Render(timeAgo)))
		}
	}

	if len(m.Sessions) > limit {
		moreStyle := DimStyle.Italic(true)
		b.WriteString(moreStyle.Render(fmt.Sprintf("\n  ...and %d more", len(m.Sessions)-limit)))
	}

	return b.String()
}

// dashboardShortPath abbreviates a workspace root path for display.
func dashboardShortPath(root string) string {
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

// RunDashboard runs the dashboard and returns the selected action
func RunDashboard(model DashboardModel) (string, error) {
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m, err := p.Run()
	if err != nil {
		return "", fmt.Errorf("failed to run dashboard: %w", err)
	}

	finalModel := m.(DashboardModel)
	if finalModel.Cancelled {
		return "", nil
	}

	return finalModel.Selected, nil
}
