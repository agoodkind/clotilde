package ui

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// TableCell is one cell's text and style.
type TableCell struct {
	Text  string
	Style tcell.Style
}

// TableWidget renders a fixed-header, scrollable, selectable table.
// It has no borders. Header underline separates from data.
type TableWidget struct {
	Headers []string      // column labels (no indicator)
	Rows    [][]TableCell // data rows
	ColGaps int           // spaces between columns (default 2)
	Rect    Rect          // last drawn rect, used for hit testing

	// Selection and scroll
	Active      bool // true after first user interaction (shows highlight)
	SelectedRow int  // 0 based data row index
	Offset      int  // top visible data row index

	// Sort state (visual only; caller toggles via SortCol/SortAsc)
	SortCol int
	SortAsc bool

	// Callbacks
	OnActivate func(row int) // double click or Enter
	OnSelect   func(row int) // single click or selection change
	OnHeader   func(col int) // clicked header; caller toggles sort
}

// NewTableWidget constructs with sensible defaults.
func NewTableWidget(headers []string) *TableWidget {
	return &TableWidget{
		Headers: headers,
		ColGaps: 2,
		SortCol: -1,
	}
}

// ColumnWidths returns the max width of each column across headers and rows.
func (t *TableWidget) ColumnWidths() []int {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = runeCount(h) + 2 // room for sort indicator
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if n := runeCount(cell.Text); n > widths[i] {
				widths[i] = n
			}
		}
	}
	return widths
}

// ContentWidth returns total drawing width: columns plus gaps.
func (t *TableWidget) ContentWidth() int {
	widths := t.ColumnWidths()
	total := 0
	for i, w := range widths {
		if i > 0 {
			total += t.ColGaps
		}
		total += w
	}
	return total
}

// visibleRows returns how many data rows fit given the table rect.
func (t *TableWidget) visibleRows() int {
	return imax(0, t.Rect.H-1) // reserve one line for header
}

// Draw renders the table into r.
func (t *TableWidget) Draw(scr tcell.Screen, r Rect) {
	t.Rect = r
	clearRect(scr, r)

	widths := t.ColumnWidths()

	// Header row with sort indicator
	x := r.X
	for i, h := range t.Headers {
		if i > 0 {
			x += t.ColGaps
		}
		label := h
		if i == t.SortCol {
			if t.SortAsc {
				label += " ^"
			} else {
				label += " v"
			}
		}
		var text string
		if i == len(t.Headers)-1 {
			text = fmt.Sprintf("%*s", widths[i], label)
		} else {
			text = fmt.Sprintf("%-*s", widths[i], label)
		}
		drawString(scr, x, r.Y, StyleHeader, text, r.X+r.W-x)
		x += widths[i]
	}

	// Data rows
	vis := t.visibleRows()
	for vi := 0; vi < vis; vi++ {
		di := t.Offset + vi
		if di >= len(t.Rows) {
			break
		}
		row := t.Rows[di]
		y := r.Y + 1 + vi
		isSel := t.Active && di == t.SelectedRow

		if isSel {
			fillRow(scr, r.X, y, r.W, StyleSelected)
		}

		x := r.X
		for i := 0; i < len(t.Headers); i++ {
			if i > 0 {
				x += t.ColGaps
			}
			var cell TableCell
			if i < len(row) {
				cell = row[i]
			}
			style := cell.Style
			if isSel {
				style = StyleSelected
			}
			var text string
			if i == len(t.Headers)-1 {
				text = fmt.Sprintf("%*s", widths[i], cell.Text)
			} else {
				text = fmt.Sprintf("%-*s", widths[i], cell.Text)
			}
			drawString(scr, x, y, style, text, r.X+r.W-x)
			x += widths[i]
		}
	}
}

// ensureVisible scrolls so SelectedRow is within the viewport.
func (t *TableWidget) ensureVisible() {
	vis := t.visibleRows()
	if vis <= 0 || len(t.Rows) == 0 {
		return
	}
	if t.SelectedRow < t.Offset {
		t.Offset = t.SelectedRow
	} else if t.SelectedRow >= t.Offset+vis {
		t.Offset = t.SelectedRow - vis + 1
	}
	t.Offset = clamp(t.Offset, 0, imax(0, len(t.Rows)-vis))
}

// MoveUp moves selection up by n rows.
func (t *TableWidget) MoveUp(n int) {
	if len(t.Rows) == 0 {
		return
	}
	t.Active = true
	t.SelectedRow = clamp(t.SelectedRow-n, 0, len(t.Rows)-1)
	t.ensureVisible()
	if t.OnSelect != nil {
		t.OnSelect(t.SelectedRow)
	}
}

// MoveDown moves selection down by n rows.
func (t *TableWidget) MoveDown(n int) {
	if len(t.Rows) == 0 {
		return
	}
	t.Active = true
	t.SelectedRow = clamp(t.SelectedRow+n, 0, len(t.Rows)-1)
	t.ensureVisible()
	if t.OnSelect != nil {
		t.OnSelect(t.SelectedRow)
	}
}

// ScrollUp scrolls viewport without moving selection (wheel).
func (t *TableWidget) ScrollUp(n int) {
	vis := t.visibleRows()
	maxOff := imax(0, len(t.Rows)-vis)
	t.Offset = clamp(t.Offset-n, 0, maxOff)
}

// ScrollDown scrolls viewport without moving selection (wheel).
func (t *TableWidget) ScrollDown(n int) {
	vis := t.visibleRows()
	maxOff := imax(0, len(t.Rows)-vis)
	t.Offset = clamp(t.Offset+n, 0, maxOff)
}

// SelectAt sets the selected row (activates if not already).
func (t *TableWidget) SelectAt(row int) {
	if row < 0 || row >= len(t.Rows) {
		return
	}
	t.Active = true
	t.SelectedRow = row
	t.ensureVisible()
	if t.OnSelect != nil {
		t.OnSelect(row)
	}
}

// RowAtY converts an absolute screen y into a data row index.
// Returns -1 if the y hits the header or lies outside the table.
func (t *TableWidget) RowAtY(y int) int {
	if y < t.Rect.Y+1 || y >= t.Rect.Y+t.Rect.H {
		return -1
	}
	di := (y - t.Rect.Y - 1) + t.Offset
	if di < 0 || di >= len(t.Rows) {
		return -1
	}
	return di
}

// ColAtX converts an absolute screen x into a column index.
// Returns -1 if outside the table. Uses current column widths and gaps.
func (t *TableWidget) ColAtX(x int) int {
	if x < t.Rect.X || x >= t.Rect.X+t.Rect.W {
		return -1
	}
	widths := t.ColumnWidths()
	pos := t.Rect.X
	for i, w := range widths {
		if i > 0 {
			pos += t.ColGaps
		}
		if x >= pos && x < pos+w {
			return i
		}
		pos += w
	}
	if len(widths) > 0 {
		return len(widths) - 1
	}
	return -1
}

// HandleEvent handles keyboard events for the table.
// Mouse events are handled at the app level so overlays can take priority.
func (t *TableWidget) HandleEvent(ev tcell.Event) bool {
	ek, ok := ev.(*tcell.EventKey)
	if !ok {
		return false
	}
	switch ek.Key() {
	case tcell.KeyUp:
		t.MoveUp(1)
		return true
	case tcell.KeyDown:
		t.MoveDown(1)
		return true
	case tcell.KeyPgUp:
		t.MoveUp(imax(1, t.visibleRows()-1))
		return true
	case tcell.KeyPgDn:
		t.MoveDown(imax(1, t.visibleRows()-1))
		return true
	case tcell.KeyHome:
		t.MoveUp(len(t.Rows))
		return true
	case tcell.KeyEnd:
		t.MoveDown(len(t.Rows))
		return true
	case tcell.KeyEnter:
		if t.Active && t.OnActivate != nil {
			t.OnActivate(t.SelectedRow)
		}
		return true
	case tcell.KeyRune:
		switch ek.Rune() {
		case 'j':
			t.MoveDown(1)
			return true
		case 'k':
			t.MoveUp(1)
			return true
		case 'g':
			t.MoveUp(len(t.Rows))
			return true
		case 'G':
			t.MoveDown(len(t.Rows))
			return true
		}
	}
	return false
}
