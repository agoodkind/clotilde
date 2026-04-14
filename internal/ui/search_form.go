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

var depthDescriptions = map[string]string{
	"quick":       "embedding only, ~20s",
	"normal":      "embedding + LLM sweep, ~4min",
	"deep":        "embedding + LLM + rerank, ~5min",
	"extra-deep":  "adds large model verification, 20min+",
}

// SearchFormModel is a BubbleTea model for the search parameter form.
// It collects session, query, and depth then quits with the result.
type SearchFormModel struct {
	Sessions  []*session.Session
	selected  *session.Session
	query     textinput.Model
	depthIdx  int
	focus     searchField
	result    SearchFormResult
	done      bool
	width     int

	// set by RunSearchForm to signal that picker should run
	needPicker bool
}

// NewSearchForm creates a search form pre-populated with sessions.
// If initial is non-nil it is pre-selected as the session.
func NewSearchForm(sessions []*session.Session, initial *session.Session) SearchFormModel {
	ti := textinput.New()
	ti.Placeholder = "What are you looking for?"
	ti.CharLimit = 512
	ti.Width = 60

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
		m.width = msg.Width
		if m.width > 10 {
			m.query.Width = min(60, m.width-20)
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.result = SearchFormResult{Cancelled: true}
			m.done = true
			return m, tea.Quit

		case "tab", "down":
			m = m.nextField()
			return m, nil

		case "shift+tab", "up":
			m = m.prevField()
			return m, nil

		case "enter":
			switch m.focus {
			case fieldSession:
				// Signal caller to run the session picker
				m.needPicker = true
				m.done = true
				return m, tea.Quit
			case fieldQuery, fieldDepth:
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

		case "left", "h":
			if m.focus == fieldDepth {
				if m.depthIdx > 0 {
					m.depthIdx--
				}
				return m, nil
			}

		case "right", "l":
			if m.focus == fieldDepth {
				if m.depthIdx < len(depthOptions)-1 {
					m.depthIdx++
				}
				return m, nil
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

func (m SearchFormModel) View() string {
	var b strings.Builder

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(SuccessColor).Padding(1, 0)
	b.WriteString(titleStyle.Render("Search Conversation"))
	b.WriteString("\n")

	sep := DimStyle.Render(strings.Repeat("─", 50))
	b.WriteString(sep + "\n\n")

	// Field: Session
	b.WriteString(m.renderFieldLabel("Session", m.focus == fieldSession))
	sessionName := "(none)"
	if m.selected != nil {
		sessionName = m.selected.DisplayName()
	}
	sessionVal := lipgloss.NewStyle().Foreground(InfoColor).Render(sessionName)
	hint := DimStyle.Render("  [enter to change]")
	if m.focus == fieldSession {
		hint = lipgloss.NewStyle().Foreground(SuccessColor).Render("  [enter to pick session]")
	}
	b.WriteString(fmt.Sprintf("  %s%s\n\n", sessionVal, hint))

	// Field: Query
	b.WriteString(m.renderFieldLabel("Query", m.focus == fieldQuery))
	b.WriteString("  " + m.query.View() + "\n\n")

	// Field: Depth
	b.WriteString(m.renderFieldLabel("Depth", m.focus == fieldDepth))
	b.WriteString("  " + m.renderDepthSelector() + "\n")
	desc := depthDescriptions[depthOptions[m.depthIdx]]
	b.WriteString("  " + DimStyle.Render(desc) + "\n\n")

	b.WriteString(sep + "\n")

	// Footer hints
	var hints []string
	hints = append(hints, "tab/shift-tab to move")
	if m.focus == fieldDepth {
		hints = append(hints, "left/right to change depth")
	}
	if m.focus == fieldSession {
		hints = append(hints, "enter to pick session")
	} else {
		hints = append(hints, "enter to search")
	}
	hints = append(hints, "esc to cancel")
	b.WriteString(DimStyle.Italic(true).Render(strings.Join(hints, " · ")))

	return b.String()
}

func (m SearchFormModel) renderFieldLabel(label string, focused bool) string {
	style := DimStyle
	prefix := "  "
	if focused {
		style = lipgloss.NewStyle().Bold(true).Foreground(SuccessColor)
		prefix = "> "
	}
	return prefix + style.Render(label+":") + "\n"
}

func (m SearchFormModel) renderDepthSelector() string {
	var parts []string
	for i, d := range depthOptions {
		if i == m.depthIdx {
			if m.focus == fieldDepth {
				parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(SuccessColor).Render("["+d+"]"))
			} else {
				parts = append(parts, lipgloss.NewStyle().Bold(true).Foreground(InfoColor).Render("["+d+"]"))
			}
		} else {
			parts = append(parts, DimStyle.Render(d))
		}
	}
	return strings.Join(parts, "  ")
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
