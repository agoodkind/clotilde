package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	bl "github.com/winder/bubblelayout"
)

var (
	viewerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	viewerFootStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ViewerModel is a scrollable text viewer using BubbleTea viewport.
type ViewerModel struct {
	Title       string
	Content     string
	viewport    viewport.Model
	ready       bool
	layout      bl.BubbleLayout
	headerID    bl.ID
	contentID   bl.ID
	footerID    bl.ID
	layoutReady bool
}

// NewViewer creates a new scrollable text viewer.
func NewViewer(title, content string) ViewerModel {
	m := ViewerModel{Title: title, Content: content}
	m.layout = bl.New()
	m.headerID = m.layout.Dock(bl.Dock{Cardinal: bl.NORTH, Min: 1, Preferred: 1, Max: 1})
	m.footerID = m.layout.Dock(bl.Dock{Cardinal: bl.SOUTH, Min: 1, Preferred: 1, Max: 1})
	m.contentID = m.layout.Add("grow")
	m.layoutReady = true
	return m
}

func (m ViewerModel) Init() tea.Cmd {
	return nil
}

func (m ViewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		layoutMsg := m.layout.Resize(msg.Width, msg.Height)
		if sz, err := layoutMsg.Size(m.contentID); err == nil {
			if !m.ready {
				m.viewport = viewport.New(sz.Width, sz.Height)
				m.viewport.SetContent(m.Content)
				m.ready = true
			} else {
				m.viewport.Width = sz.Width
				m.viewport.Height = sz.Height
			}
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
	return header + ViewportWithScrollbar(m.viewport) + "\n" + footer
}

// RunViewer runs the scrollable text viewer.
func RunViewer(model ViewerModel) error {
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
