package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var (
	viewerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	viewerFootStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ViewerModel is a scrollable text viewer using BubbleTea viewport.
type ViewerModel struct {
	Title    string
	Content  string
	viewport viewport.Model
	ready    bool
}

// NewViewer creates a new scrollable text viewer.
func NewViewer(title, content string) ViewerModel {
	return ViewerModel{Title: title, Content: content}
}

func (m ViewerModel) Init() tea.Cmd {
	return nil
}

func (m ViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		headerHeight := 2
		footerHeight := 1
		if !m.ready {
			m.viewport = viewport.New(msg.Width, msg.Height-headerHeight-footerHeight)
			m.viewport.SetContent(m.Content)
			m.ready = true
		} else {
			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - headerHeight - footerHeight
		}
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		}
	}

	var cmd tea.Cmd
	m.viewport, cmd = m.viewport.Update(msg)
	return m, cmd
}

func (m ViewerModel) View() string {
	if !m.ready {
		return "Loading..."
	}
	header := viewerTitleStyle.Render(m.Title) + "\n"
	footer := viewerFootStyle.Render(fmt.Sprintf(" %d%% · q to close", int(m.viewport.ScrollPercent()*100)))
	return header + m.viewport.View() + "\n" + footer
}

// RunViewer runs the scrollable text viewer.
func RunViewer(model ViewerModel) error {
	p := tea.NewProgram(model, tea.WithAltScreen())
	_, err := p.Run()
	return err
}
