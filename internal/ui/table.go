package ui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// TableModel represents a table with headers, rows, and cursor navigation
type TableModel struct {
	Headers        []string
	Rows           [][]string
	Selected       int      // -1 if cancelled
	SelectedRow    []string // actual selected row data
	Cancelled      bool
	SortColumn     int  // -1 for no sort, 0+ for column index
	SortAscending  bool // true for ascending, false for descending
	Nav            ListNav
	Filter         FilterState
	sortingEnabled bool // whether sorting is enabled
}

// NewTable creates a new table model
func NewTable(headers []string, rows [][]string) TableModel {
	return TableModel{
		Headers:    headers,
		Rows:       rows,
		Selected:   -1,
		SortColumn: -1, // No sorting by default
	}
}

// WithSorting enables sorting on this table
func (m TableModel) WithSorting() TableModel {
	m.sortingEnabled = true
	return m
}

// Init initializes the model (required by bubbletea)
func (m TableModel) Init() tea.Cmd {
	return nil
}

// Update handles keyboard input
func (m TableModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()

		// Handle filter mode separately
		if m.Filter.Active {
			if m.Filter.HandleFilterKey(key, msg.Runes) {
				m.Nav.Cursor = 0 // Reset cursor when filter changes
			}
			return m, nil
		}

		// Quit keys
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
			filtered := m.filteredRows()
			if len(filtered) > 0 && m.Nav.Cursor < len(filtered) {
				m.Selected = m.Nav.Cursor
				m.SelectedRow = filtered[m.Nav.Cursor]
			}
			return m, tea.Quit
		}

		// Navigation
		m.Nav.Total = len(m.filteredRows())
		if m.Nav.HandleKey(key) {
			return m, nil
		}

		// Handle number keys for sorting (1, 2, 3... for columns)
		if m.sortingEnabled && len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if r >= '1' && r <= '9' {
				colIndex := int(r - '1')
				if colIndex < len(m.Headers) {
					if m.SortColumn == colIndex {
						m.SortAscending = !m.SortAscending
					} else {
						m.SortColumn = colIndex
						m.SortAscending = true
					}
					m.sortRows()
					m.Nav.Cursor = 0
				}
				return m, nil
			}
		}
	}
	return m, nil
}

// View renders the table
func (m TableModel) View() string {
	var b strings.Builder

	// Filter input
	b.WriteString(m.Filter.RenderFilterInput())

	// Get filtered rows
	filtered := m.filteredRows()

	// No rows (after filtering)
	if len(filtered) == 0 {
		b.WriteString(RenderEmptyState(m.Filter.Text, "data"))
		b.WriteString("\n\n")
		b.WriteString(DimStyle.Render("Press / to filter, q to quit"))
		return b.String()
	}

	// Calculate column widths
	widths := m.calculateColumnWidths()

	// Render header
	b.WriteString(m.renderHeaderRow(widths))
	b.WriteString("\n")
	b.WriteString(m.renderSeparator(widths))
	b.WriteString("\n")

	// Render rows
	for i, row := range filtered {
		rowStr := m.renderRow(row, widths)
		b.WriteString(RenderCursorLine(i, m.Nav.Cursor, rowStr))
		b.WriteString("\n")
	}

	// Help text
	b.WriteString("\n")
	switch {
	case m.Filter.Text != "":
		b.WriteString(RenderHelpBar("(Esc to clear filter, / to edit, ↑/↓ to navigate, enter to select)"))
	case m.sortingEnabled:
		b.WriteString(RenderHelpBar("(↑/↓ or j/k to navigate, / to filter, 1-9 to sort, enter to select, q to quit)"))
	default:
		b.WriteString(RenderHelpBar("(↑/↓ or j/k to navigate, / to filter, enter to select, q to quit)"))
	}

	return b.String()
}

// calculateColumnWidths determines the width of each column
func (m TableModel) calculateColumnWidths() []int {
	if len(m.Headers) == 0 {
		return []int{}
	}

	widths := make([]int, len(m.Headers))

	// Start with header widths (including indicators)
	for i, header := range m.Headers {
		headerText := header

		// Add sort indicator if this column is being sorted
		if m.SortColumn == i {
			headerText += " ↑" // Both ↑ and ↓ are same width
		}

		// Add column number hint if sorting is enabled
		if m.sortingEnabled {
			headerText = fmt.Sprintf("%s [%d]", headerText, i+1)
		}

		widths[i] = len(headerText)
	}

	// Check row widths
	for _, row := range m.Rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	return widths
}

// renderHeaderRow renders the header row
func (m TableModel) renderHeaderRow(widths []int) string {
	var cells []string
	for i, header := range m.Headers {
		width := widths[i]
		headerText := header

		// Add sort indicator if this column is being sorted
		if m.SortColumn == i {
			if m.SortAscending {
				headerText = header + " ↑"
			} else {
				headerText = header + " ↓"
			}
		}

		// Add column number hint if sorting is enabled
		if m.sortingEnabled {
			headerText = fmt.Sprintf("%s [%d]", headerText, i+1)
		}

		cell := BoldStyle.Render(padRight(headerText, width))
		cells = append(cells, cell)
	}
	return strings.Join(cells, "  ")
}

// renderSeparator renders the separator line between header and rows
func (m TableModel) renderSeparator(widths []int) string {
	var parts []string
	for _, width := range widths {
		parts = append(parts, strings.Repeat("─", width))
	}
	return DimStyle.Render(strings.Join(parts, "  "))
}

// renderRow renders a single data row
func (m TableModel) renderRow(row []string, widths []int) string {
	var cells []string
	for i, cell := range row {
		if i < len(widths) {
			width := widths[i]
			cells = append(cells, padRight(cell, width))
		}
	}
	return strings.Join(cells, "  ")
}

// filteredRows returns rows that match the current filter
func (m TableModel) filteredRows() [][]string {
	if m.Filter.Text == "" {
		return m.Rows
	}

	var filtered [][]string
	lowerFilter := strings.ToLower(m.Filter.Text)

	for _, row := range m.Rows {
		for _, cell := range row {
			if strings.Contains(strings.ToLower(cell), lowerFilter) {
				filtered = append(filtered, row)
				break
			}
		}
	}

	return filtered
}

// sortRows sorts the rows based on SortColumn and SortAscending
func (m *TableModel) sortRows() {
	if m.SortColumn < 0 || m.SortColumn >= len(m.Headers) {
		return
	}

	// Simple bubble sort - good enough for typical row counts
	for i := range len(m.Rows) - 1 {
		for j := range len(m.Rows) - i - 1 {
			// Get values to compare
			val1 := ""
			val2 := ""
			if m.SortColumn < len(m.Rows[j]) {
				val1 = m.Rows[j][m.SortColumn]
			}
			if m.SortColumn < len(m.Rows[j+1]) {
				val2 = m.Rows[j+1][m.SortColumn]
			}

			// Compare and swap if needed
			var shouldSwap bool
			if m.SortAscending {
				shouldSwap = val1 > val2
			} else {
				shouldSwap = val1 < val2
			}

			if shouldSwap {
				m.Rows[j], m.Rows[j+1] = m.Rows[j+1], m.Rows[j]
			}
		}
	}
}

// padRight pads a string with spaces to reach the desired width
func padRight(s string, width int) string {
	if len(s) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(s))
}

// RunTable runs the table and returns the selected row data (or nil if cancelled)
func RunTable(model TableModel) ([]string, error) {
	p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithMouseCellMotion())
	m, err := p.Run()
	if err != nil {
		return nil, fmt.Errorf("failed to run table: %w", err)
	}

	finalModel := m.(TableModel)
	if finalModel.Cancelled {
		return nil, nil
	}

	return finalModel.SelectedRow, nil
}
