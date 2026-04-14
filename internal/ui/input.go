package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// InputModel is a simple single-line text input.
type InputModel struct {
	input     textinput.Model
	Prompt    string
	Value     string
	Cancelled bool
}

// NewInput creates a new text input with a prompt.
func NewInput(prompt string) InputModel {
	ti := textinput.New()
	ti.Placeholder = "Type here..."
	ti.Focus()
	return InputModel{input: ti, Prompt: prompt}
}

func (m InputModel) Init() tea.Cmd {
	return textinput.Blink
}

func (m InputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			m.Value = m.input.Value()
			return m, tea.Quit
		case "ctrl+c", "esc":
			m.Cancelled = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m InputModel) View() string {
	return fmt.Sprintf("%s\n\n%s\n\n(enter to search, esc to cancel)", m.Prompt, m.input.View())
}

// RunInput runs the text input and returns the entered value.
// Returns empty string if cancelled.
func RunInput(model InputModel) (string, error) {
	p := tea.NewProgram(model)
	result, err := p.Run()
	if err != nil {
		return "", err
	}
	final := result.(InputModel)
	if final.Cancelled {
		return "", nil
	}
	return final.Value, nil
}
