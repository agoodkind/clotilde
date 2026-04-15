package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fgrehm/clotilde/internal/session"
)

// SearchFormResult holds the parameters collected by SearchFormModel.
type SearchFormResult struct {
	Session   *session.Session
	Query     string
	Depth     string
	Cancelled bool
}

// searchField enumerates the focusable fields on the search form.
type searchField int

const (
	fieldSession searchField = iota
	fieldQuery
	fieldDepth
)

var depthOptions = []string{"quick", "normal", "deep", "extra-deep"}

// depthDescriptions holds a short inline description for each depth level.
var depthDescriptions = map[string]string{
	"quick":      "embedding similarity only (fastest, ~2 seconds)",
	"normal":     "embedding filter + LLM sweep (moderate, ~30 seconds)",
	"deep":       "embedding + sweep + rerank + deep analysis (thorough, ~2-5 minutes)",
	"extra-deep": "full pipeline with largest model (~10+ minutes)",
}

// SearchFormModel is a BubbleTea model for the search parameter form.
// It collects session, query, and depth then quits with the result.
type SearchFormModel struct {
	Sessions []*session.Session
	selected *session.Session
	query    textinput.Model
	depthIdx int
	focus    searchField
	result   SearchFormResult
	done     bool
	term     TermSize

	// set by RunSearchForm to signal that picker should run
	needPicker bool
}

// NewSearchForm creates a search form pre-populated with sessions.
// If initial is non-nil it is pre-selected as the session.
func NewSearchForm(sessions []*session.Session, initial *session.Session) SearchFormModel {
	ti := textinput.New()
	ti.Placeholder = "What are you looking for?"
	ti.CharLimit = 512
	ti.Width = 70

	m := SearchFormModel{
		Sessions: sessions,
		selected: initial,
		query:    ti,
		depthIdx: 0,
		focus:    fieldQuery,
	}

	if initial == nil && len(sessions) > 0 {
		m.selected = sessions[0]
	}

	if m.focus == fieldQuery {
		m.query.Focus()
	}

	return m
}

func (m SearchFormModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m SearchFormModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.term.HandleResize(msg)
		if m.term.Width > 10 {
			m.query.Width = min(70, m.term.Width-10)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.result = SearchFormResult{Cancelled: true}
			m.done = true
			return m, tea.Quit

		case "tab":
			m = m.nextField()
			return m, nil

		case "shift+tab":
			m = m.prevField()
			return m, nil

		case "down":
			if m.focus == fieldDepth {
				if m.depthIdx < len(depthOptions)-1 {
					m.depthIdx++
				}
				return m, nil
			}
			m = m.nextField()
			return m, nil

		case "up":
			if m.focus == fieldDepth {
				if m.depthIdx > 0 {
					m.depthIdx--
				}
				return m, nil
			}
			m = m.prevField()
			return m, nil

		case "enter", " ":
			switch m.focus {
			case fieldSession:
				// Signal caller to run the session picker
				m.needPicker = true
				m.done = true
				return m, tea.Quit
			case fieldDepth:
				// space/enter on depth just moves to next field
				if msg.String() == "enter" {
					if m.selected == nil || strings.TrimSpace(m.query.Value()) == "" {
						return m, nil
					}
					m.result = SearchFormResult{
						Session: m.selected,
						Query:   strings.TrimSpace(m.query.Value()),
						Depth:   depthOptions[m.depthIdx],
					}
					m.done = true
					return m, tea.Quit
				}
				return m, nil
			case fieldQuery:
				if msg.String() == " " {
					// pass space through to text input
					break
				}
				if m.selected == nil || strings.TrimSpace(m.query.Value()) == "" {
					return m, nil
				}
				m.result = SearchFormResult{
					Session: m.selected,
					Query:   strings.TrimSpace(m.query.Value()),
					Depth:   depthOptions[m.depthIdx],
				}
				m.done = true
				return m, tea.Quit
			}
		}
	}

	if m.focus == fieldQuery {
		var cmd tea.Cmd
		m.query, cmd = m.query.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m SearchFormModel) nextField() SearchFormModel {
	m.focus = (m.focus + 1) % 3
	m.syncFocus()
	return m
}

func (m SearchFormModel) prevField() SearchFormModel {
	m.focus = (m.focus + 2) % 3
	m.syncFocus()
	return m
}

func (m *SearchFormModel) syncFocus() {
	if m.focus == fieldQuery {
		m.query.Focus()
	} else {
		m.query.Blur()
	}
}

// sectionWidth returns the inner width for section boxes.
func (m SearchFormModel) sectionWidth() int {
	w := 78
	if m.term.Width > 10 {
		w = min(78, m.term.Width-4)
	}
	return w
}

// renderSection wraps content in a rounded border with an optional active highlight.
func (m SearchFormModel) renderSection(label, content string, focused bool) string {
	borderColor := MutedColor
	labelStyle := DimStyle.Bold(false)
	if focused {
		borderColor = SuccessColor
		labelStyle = lipgloss.NewStyle().Bold(true).Foreground(SuccessColor)
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(m.sectionWidth())

	header := labelStyle.Render(label)
	inner := lipgloss.JoinVertical(lipgloss.Left, header, content)
	return box.Render(inner)
}

func (m SearchFormModel) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(SuccessColor).MarginBottom(1)
	b.WriteString(titleStyle.Render("Search Conversation"))
	b.WriteString("\n\n")

	// --- Session section ---
	var sessionContent string
	if m.selected != nil {
		sessionContent = lipgloss.NewStyle().Bold(true).Foreground(InfoColor).Render(m.selected.Name)
		if m.focus == fieldSession {
			sessionContent += DimStyle.Render("  (press Enter to change)")
		}
	} else {
		sessionContent = DimStyle.Italic(true).Render("Press Enter to pick a session...")
	}
	b.WriteString(m.renderSection("Session", sessionContent, m.focus == fieldSession))
	b.WriteString("\n")

	// --- Query section ---
	queryContent := m.query.View()
	b.WriteString(m.renderSection("Query", queryContent, m.focus == fieldQuery))
	b.WriteString("\n")

	// --- Depth section ---
	depthContent := m.renderDepthRadio()
	b.WriteString(m.renderSection("Search Depth", depthContent, m.focus == fieldDepth))
	b.WriteString("\n")

	// --- Status bar ---
	statusBar := m.renderStatusBar()
	b.WriteString(statusBar)

	return b.String()
}

func (m SearchFormModel) renderDepthRadio() string {
	var rows []string
	for i, d := range depthOptions {
		selected := i == m.depthIdx
		focused := m.focus == fieldDepth

		radio := "○"
		if selected {
			radio = "●"
		}

		var nameStyle lipgloss.Style
		var descStyle lipgloss.Style

		switch {
		case selected && focused:
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(SuccessColor)
			descStyle = lipgloss.NewStyle().Foreground(SuccessColor)
			radio = lipgloss.NewStyle().Foreground(SuccessColor).Bold(true).Render(radio)
		case selected:
			nameStyle = lipgloss.NewStyle().Bold(true).Foreground(InfoColor)
			descStyle = lipgloss.NewStyle().Foreground(InfoColor)
			radio = lipgloss.NewStyle().Foreground(InfoColor).Bold(true).Render(radio)
		case focused:
			nameStyle = DimStyle
			descStyle = DimStyle
			radio = DimStyle.Render(radio)
		default:
			nameStyle = DimStyle
			descStyle = DimStyle
			radio = DimStyle.Render(radio)
		}

		desc := depthDescriptions[d]
		row := fmt.Sprintf("%s %s  %s", radio, nameStyle.Render(d), descStyle.Render(desc))
		rows = append(rows, row)
	}
	return strings.Join(rows, "\n")
}

func (m SearchFormModel) renderStatusBar() string {
	var parts []string

	parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("Tab")+" next field")
	parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("Shift+Tab")+" prev field")

	if m.focus == fieldDepth {
		parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("↑↓")+" select depth")
	}

	if m.focus == fieldSession {
		parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("Enter")+" pick session")
	} else {
		parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("Enter")+" submit")
	}

	parts = append(parts, lipgloss.NewStyle().Foreground(InfoColor).Render("Esc")+" cancel")

	bar := strings.Join(parts, DimStyle.Render(" · "))
	return DimStyle.Render(bar)
}

// RunSearchForm runs the search parameter form.
// Returns nil result (with Cancelled=true) if the user exits without searching.
// If the user wants to change the session, it re-runs the picker and shows the form again.
func RunSearchForm(sessions []*session.Session, initial *session.Session, previewFn PreviewFunc) (*SearchFormResult, error) {
	selected := initial
	if selected == nil && len(sessions) > 0 {
		selected = sessions[0]
	}

	for {
		model := NewSearchForm(sessions, selected)
		p := tea.NewProgram(model, tea.WithAltScreen())
		raw, err := p.Run()
		if err != nil {
			return nil, fmt.Errorf("search form: %w", err)
		}

		final := raw.(SearchFormModel)

		if !final.needPicker {
			// User either cancelled or submitted
			return &final.result, nil
		}

		// User pressed enter on the session field: show picker
		picker := NewPicker(sessions, "Select session to search").WithPreview()
		picker.PreviewFn = previewFn
		picked, pickErr := RunPicker(picker)
		if pickErr != nil {
			return &SearchFormResult{Cancelled: true}, nil
		}
		if picked == nil {
			// Picker cancelled: reopen form with same session
			continue
		}
		selected = picked
	}
}
