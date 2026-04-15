package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/fgrehm/clotilde/internal/transcript"
)

const (
	maxContextTokens = 1_000_000 // 1M token context window constant
	sliderWidth      = 30        // character width of the progress bar
	previewCount     = 5         // number of preview messages to show
)

// focusField enumerates which UI field has keyboard focus.
const (
	focusSlider  = 0
	focusOptions = 1
	focusActions = 2 // reserved; enter/esc always work globally
)

// CompactChoices holds the user's selections after the TUI exits.
type CompactChoices struct {
	BoundaryPercent  int
	StripToolResults bool
	StripThinking    bool
	StripLargeInputs bool
	Applied          bool
	DryRun           bool
	Cancelled        bool
}

// CompactModel is the BubbleTea model for the interactive compact UI.
type CompactModel struct {
	sessionName    string
	transcriptPath string

	// data loaded once on init
	chainLines []int
	allLines   []string
	boundaries []int

	// slider state
	boundaryPercent int // 0-100; percent of chain that is VISIBLE

	// checkbox state
	stripToolResults bool
	stripThinking    bool
	stripLargeInputs bool

	// computed display values
	estimatedTokens int
	visibleEntries  int
	previewMessages []string

	// focus
	focusField  int // focusSlider or focusOptions
	optionIndex int // which checkbox is highlighted (0-2)

	// outcome
	applied   bool
	dryRun    bool
	cancelled bool

	// terminal size
	term TermSize

	// error message to display
	errMsg string
}

// NewCompactModel builds an initial CompactModel from pre-loaded transcript data.
func NewCompactModel(sessionName, transcriptPath string, chainLines []int, allLines []string) CompactModel {
	boundaries := transcript.FindBoundaries(allLines)

	m := CompactModel{
		sessionName:     sessionName,
		transcriptPath:  transcriptPath,
		chainLines:      chainLines,
		allLines:        allLines,
		boundaries:      boundaries,
		boundaryPercent: 30, // default: show 30% of chain
		focusField:      focusSlider,
	}
	m.recompute()
	return m
}

// recompute recalculates derived fields whenever the slider or options change.
func (m *CompactModel) recompute() {
	total := len(m.chainLines)
	if total == 0 {
		return
	}

	// boundaryPercent == percent VISIBLE means we keep the last N% of the chain.
	// targetStep is the index within chainLines where the boundary sits.
	targetStep := total - (total * m.boundaryPercent / 100)
	if targetStep < 1 {
		targetStep = 1
	}
	if targetStep >= total {
		targetStep = total - 1
	}

	m.visibleEntries = total - targetStep

	// Token estimate for the visible portion
	visible := m.chainLines[targetStep:]
	tokens, err := transcript.EstimateTokens(m.allLines, visible)
	if err != nil {
		m.estimatedTokens = 0
	} else {
		m.estimatedTokens = tokens
	}

	m.previewMessages = transcript.PreviewMessages(m.allLines, m.chainLines, targetStep, previewCount)
}

// targetStep returns the chain index for the current boundaryPercent.
func (m CompactModel) targetStep() int {
	total := len(m.chainLines)
	if total == 0 {
		return 0
	}
	step := total - (total * m.boundaryPercent / 100)
	if step < 1 {
		step = 1
	}
	if step >= total {
		step = total - 1
	}
	return step
}

// Init satisfies tea.Model.
func (m CompactModel) Init() tea.Cmd {
	return nil
}

// Update handles key events and window resizes.
func (m CompactModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.term.HandleResize(msg)
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, tea.Quit

		case "enter":
			m.applied = true
			return m, tea.Quit

		case "d":
			m.dryRun = true
			return m, tea.Quit

		case "tab":
			// Cycle focus: slider -> options -> slider
			m.focusField = (m.focusField + 1) % 2
			return m, nil

		case "shift+tab":
			m.focusField = (m.focusField + 1) % 2
			return m, nil
		}

		switch m.focusField {
		case focusSlider:
			switch msg.String() {
			case "left", "h":
				if m.boundaryPercent > 5 {
					m.boundaryPercent -= 5
				} else {
					m.boundaryPercent = 0
				}
				m.recompute()
			case "right", "l":
				if m.boundaryPercent < 95 {
					m.boundaryPercent += 5
				} else {
					m.boundaryPercent = 100
				}
				m.recompute()
			}

		case focusOptions:
			switch msg.String() {
			case "up", "k":
				if m.optionIndex > 0 {
					m.optionIndex--
				}
			case "down", "j":
				if m.optionIndex < 2 {
					m.optionIndex++
				}
			case " ":
				switch m.optionIndex {
				case 0:
					m.stripToolResults = !m.stripToolResults
				case 1:
					m.stripThinking = !m.stripThinking
				case 2:
					m.stripLargeInputs = !m.stripLargeInputs
				}
			}
		}
	}

	return m, nil
}

// View renders the compact TUI.
func (m CompactModel) View() string {
	var b strings.Builder

	// Header
	headerStyle := BoldStyle.Foreground(InfoColor)
	b.WriteString(headerStyle.Render(fmt.Sprintf("Compact: %s", m.sessionName)))
	b.WriteString("\n")
	b.WriteString(DimStyle.Render(fmt.Sprintf(
		"Chain: %d entries | Boundaries: %d",
		len(m.chainLines), len(m.boundaries),
	)))
	b.WriteString("\n\n")

	// Boundary slider
	sliderLabel := "Boundary position:"
	if m.focusField == focusSlider {
		sliderLabel = InfoStyle.Render("Boundary position:")
	} else {
		sliderLabel = BoldStyle.Render("Boundary position:")
	}
	b.WriteString(sliderLabel)
	b.WriteString(" ")
	b.WriteString(m.renderSlider())
	b.WriteString(fmt.Sprintf(" %d%%", m.boundaryPercent))
	b.WriteString("\n")

	// Token stats
	b.WriteString(DimStyle.Render(fmt.Sprintf(
		"Visible entries: %d | Est. tokens: ~%s/%s",
		m.visibleEntries,
		formatTokens(m.estimatedTokens),
		formatTokens(maxContextTokens),
	)))
	b.WriteString("\n\n")

	// Compaction options checkboxes
	optionsLabel := "Compaction level:"
	if m.focusField == focusOptions {
		optionsLabel = InfoStyle.Render("Compaction level:")
	} else {
		optionsLabel = BoldStyle.Render("Compaction level:")
	}
	b.WriteString(optionsLabel)
	b.WriteString("\n")

	options := []struct {
		label   string
		checked bool
	}{
		{"Move boundary only", true}, // always on (it's the primary action)
		{"Strip tool results", m.stripToolResults},
		{"Strip thinking blocks", m.stripThinking},
		{"Strip large inputs (>1KB)", m.stripLargeInputs},
	}

	for i, opt := range options {
		prefix := "  "
		checkBox := "[ ]"
		if opt.checked {
			checkBox = "[x]"
		}

		line := fmt.Sprintf("%s %s  %s", prefix, checkBox, opt.label)

		if m.focusField == focusOptions && i > 0 && i-1 == m.optionIndex {
			// i==0 is "Move boundary only" which is read-only; options 1-3 map to optionIndex 0-2
			line = lipgloss.NewStyle().Foreground(SuccessColor).Bold(true).Render("> " + checkBox + "  " + opt.label)
		} else if i == 0 {
			// "Move boundary only" is always shown as a non-interactive hint
			line = DimStyle.Render(fmt.Sprintf("  %s  %s", checkBox, opt.label))
		}

		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Preview messages
	if len(m.previewMessages) > 0 {
		b.WriteString(BoldStyle.Render(fmt.Sprintf("First %d messages after boundary:", len(m.previewMessages))))
		b.WriteString("\n")
		for i, msg := range m.previewMessages {
			b.WriteString(DimStyle.Render(fmt.Sprintf("  %d. %s", i+1, msg)))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(DimStyle.Render("  (no user messages in visible range)"))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	// Help bar
	helpParts := []string{
		InfoStyle.Render("[Enter]") + " Apply",
		InfoStyle.Render("[Tab]") + " Next field",
		InfoStyle.Render("[Esc]") + " Cancel",
		InfoStyle.Render("[d]") + " Dry run",
	}
	b.WriteString(DimStyle.Render(strings.Join(helpParts, "  ")))

	if m.errMsg != "" {
		b.WriteString("\n")
		b.WriteString(ErrorStyle.Render(m.errMsg))
	}

	return b.String()
}

// renderSlider renders a text progress bar for the boundary percent.
func (m CompactModel) renderSlider() string {
	filled := (m.boundaryPercent * sliderWidth) / 100
	if filled > sliderWidth {
		filled = sliderWidth
	}

	bar := strings.Repeat("=", filled)
	if filled < sliderWidth {
		bar += ">"
		bar += strings.Repeat(" ", sliderWidth-filled-1)
	}

	style := DimStyle
	if m.focusField == focusSlider {
		style = lipgloss.NewStyle().Foreground(InfoColor)
	}
	return style.Render("[" + bar + "]")
}

// formatTokens formats a token count as "310k" or "1.2M" etc.
func formatTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// Choices returns the final user choices from the model.
func (m CompactModel) Choices() CompactChoices {
	return CompactChoices{
		BoundaryPercent:  m.boundaryPercent,
		StripToolResults: m.stripToolResults,
		StripThinking:    m.stripThinking,
		StripLargeInputs: m.stripLargeInputs,
		Applied:          m.applied,
		DryRun:           m.dryRun,
		Cancelled:        m.cancelled,
	}
}

// RunCompactUI launches the interactive TUI and returns the user's choices.
// chainLines and allLines should be pre-loaded via transcript.WalkChain.
func RunCompactUI(sessionName, transcriptPath string, chainLines []int, allLines []string) (CompactChoices, error) {
	model := NewCompactModel(sessionName, transcriptPath, chainLines, allLines)
	p := tea.NewProgram(model, tea.WithAltScreen())
	result, err := p.Run()
	if err != nil {
		return CompactChoices{}, fmt.Errorf("running compact UI: %w", err)
	}

	final, ok := result.(CompactModel)
	if !ok {
		return CompactChoices{}, fmt.Errorf("unexpected model type from TUI")
	}
	return final.Choices(), nil
}
