package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FilterState handles the filter mode state machine.
// Embed in any model that needs filter/search.
type FilterState struct {
	Text   string
	Active bool
}

// HandleFilterKey processes a keypress while filter mode is active.
// Returns true if the key was consumed (caller should return early).
// Callers must check Active==true before calling this.
func (f *FilterState) HandleFilterKey(key string, runes []rune) bool {
	switch key {
	case "esc":
		f.Active = false
		f.Text = ""
		return true

	case "enter":
		f.Active = false
		return true

	case "backspace":
		if len(f.Text) > 0 {
			f.Text = f.Text[:len(f.Text)-1]
		}
		return true

	default:
		if len(runes) == 1 {
			f.Text += string(runes[0])
			return true
		}
		return false
	}
}

// RenderFilterInput renders the filter input line with cursor.
// Returns empty string if filter is not active and has no text.
func (f *FilterState) RenderFilterInput() string {
	if !f.Active && f.Text == "" {
		return ""
	}

	var b strings.Builder
	filterPrefix := "Filter: "
	if f.Active {
		filterPrefix = "Filter (type to search): "
	}
	b.WriteString(InfoStyle.Render(filterPrefix))
	b.WriteString(f.Text)
	if f.Active {
		b.WriteString("\u2588") // Block cursor
	}
	b.WriteString("\n\n")
	return b.String()
}

// ListNav handles cursor navigation for a list of N items.
type ListNav struct {
	Cursor int
	Total  int
}

// HandleKey processes up/down/home/end/j/k/g/G navigation.
// Total must be set before calling. Returns true if the key was handled.
func (n *ListNav) HandleKey(key string) bool {
	switch key {
	case "up", "k":
		if n.Cursor > 0 {
			n.Cursor--
		}
		return true

	case "down", "j":
		if n.Cursor < n.Total-1 {
			n.Cursor++
		}
		return true

	case "home", "g":
		n.Cursor = 0
		return true

	case "end", "G":
		if n.Total > 0 {
			n.Cursor = n.Total - 1
		}
		return true

	default:
		return false
	}
}

// ClampCursor ensures cursor is within bounds after Total changes.
func (n *ListNav) ClampCursor() {
	if n.Total == 0 {
		n.Cursor = 0
		return
	}
	if n.Cursor >= n.Total {
		n.Cursor = n.Total - 1
	}
	if n.Cursor < 0 {
		n.Cursor = 0
	}
}

// RenderCursorLine renders a single line with ">" prefix when selected
// and SuccessColor+bold highlight.
func RenderCursorLine(index, cursor int, text string) string {
	prefix := " "
	if index == cursor {
		prefix = ">"
		text = lipgloss.NewStyle().
			Foreground(SuccessColor).
			Bold(true).
			Render(text)
	}
	return fmt.Sprintf("%s %s", prefix, text)
}

// RenderEmptyState renders an empty state message with optional filter context.
// noun is the thing being listed (e.g. "sessions", "rows", "data").
func RenderEmptyState(filterText, noun string) string {
	var b strings.Builder
	emptyStyle := DimStyle.Italic(true)
	if filterText != "" {
		b.WriteString(emptyStyle.Render(fmt.Sprintf("No %s matching '%s'", noun, filterText)))
	} else {
		b.WriteString(emptyStyle.Render(fmt.Sprintf("No %s available", noun)))
	}
	return b.String()
}

// RenderHelpBar renders a help bar in dim italic from a pre-formatted string.
func RenderHelpBar(text string) string {
	return DimStyle.Italic(true).Render(text)
}

// HandleQuitKeys handles ctrl+c, q, esc for quit/cancel.
// Returns quit=true if the program should exit, clearFilter=true if only
// the filter should be cleared (not quit).
func HandleQuitKeys(key string, filterActive bool, filterText string) (quit bool, clearFilter bool) {
	switch key {
	case "ctrl+c":
		return true, false
	case "q":
		if !filterActive {
			return true, false
		}
	case "esc":
		if filterText != "" {
			return false, true
		}
		return true, false
	}
	return false, false
}

// TermSize holds terminal dimensions from WindowSizeMsg.
type TermSize struct {
	Width  int
	Height int
}

// HandleResize updates dimensions from a WindowSizeMsg.
func (t *TermSize) HandleResize(msg tea.WindowSizeMsg) {
	t.Width = msg.Width
	t.Height = msg.Height
}

// VisibleLines returns how many list items fit given reserved lines for header/footer.
func (t *TermSize) VisibleLines(reserved int) int {
	if t.Height <= reserved {
		return 10 // fallback minimum
	}
	return t.Height - reserved
}

// RenderScrollbar renders a vertical scrollbar for a viewport.
// Returns an empty string if the content fits without scrolling.
// The scrollbar is a single column of characters: track (dim) with a thumb (bright).
func RenderScrollbar(vp viewport.Model, height int) string {
	total := vp.TotalLineCount()
	if total <= height || height <= 0 {
		return ""
	}

	// Calculate thumb position and size
	thumbSize := max(1, height*height/total)
	scrollRange := height - thumbSize
	thumbPos := 0
	if total-height > 0 {
		thumbPos = int(float64(vp.YOffset) / float64(total-height) * float64(scrollRange))
	}
	if thumbPos > scrollRange {
		thumbPos = scrollRange
	}

	trackStyle := lipgloss.NewStyle().Foreground(MutedColor)
	thumbStyle := lipgloss.NewStyle().Foreground(InfoColor)

	var lines []string
	for i := range height {
		if i >= thumbPos && i < thumbPos+thumbSize {
			lines = append(lines, thumbStyle.Render("\u2588")) // full block
		} else {
			lines = append(lines, trackStyle.Render("\u2502")) // light vertical
		}
	}
	return strings.Join(lines, "\n")
}

// ViewportWithScrollbar renders a viewport with a scrollbar on the right edge.
// If content fits, renders the viewport alone without a scrollbar.
func ViewportWithScrollbar(vp viewport.Model) string {
	scrollbar := RenderScrollbar(vp, vp.Height)
	if scrollbar == "" {
		return vp.View()
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, vp.View(), " ", scrollbar)
}
