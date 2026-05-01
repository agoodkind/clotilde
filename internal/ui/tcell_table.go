package ui

import (
	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/uniseg"
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
	HOffset     int  // leftmost visible column in cells

	// ScrollbarRect is the last drawn scrollbar region. Zero height when
	// the table is not tall enough to need a bar. Used by the parent App
	// for click-to-jump and drag-to-scroll hit tests.
	ScrollbarRect Rect

	// LastContentWidth records the total width of all column content in
	// the most recent Draw. The parent App reads it to clamp HOffset and
	// to know whether horizontal scrolling is necessary.
	LastContentWidth int

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

// ColumnWidths returns the max display width of each column across headers and rows.
func (t *TableWidget) ColumnWidths() []int {
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		label := h
		if i == t.SortCol {
			if t.SortAsc {
				label += " ^"
			} else {
				label += " v"
			}
		}
		widths[i] = cellCount(label)
	}
	for _, row := range t.Rows {
		for i, cell := range row {
			if i >= len(widths) {
				continue
			}
			if n := cellCount(cell.Text); n > widths[i] {
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

// Draw renders the table into r. Two scrollbars appear when needed: a
// vertical bar on the right edge for row overflow, and a horizontal
// indicator through the column layout when the total content width
// exceeds r.W. HOffset controls horizontal scroll.
func (t *TableWidget) Draw(scr tcell.Screen, r Rect) {
	t.Rect = r
	clearRect(scr, r)

	vis := imax(0, r.H-1)
	needsBar := len(t.Rows) > vis && r.W > 2
	contentW := r.W
	if needsBar {
		contentW = r.W - 1
	}

	widths := t.ColumnWidths()

	// Sum total virtual width so we know when to enable horizontal scroll
	// and how far HOffset may go.
	totalW := 0
	for i, w := range widths {
		if i > 0 {
			totalW += t.ColGaps
		}
		totalW += w
	}
	t.LastContentWidth = totalW
	maxHOff := imax(0, totalW-contentW)
	if t.HOffset > maxHOff {
		t.HOffset = maxHOff
	}
	if t.HOffset < 0 {
		t.HOffset = 0
	}

	// drawShifted paints text at virtual column position vx in display cells so
	// the cell at vx == HOffset appears at the left edge. Wide runes use tcell
	// SetContent so each grapheme lands on the right number of cells.
	drawShifted := func(vx, y int, style tcell.Style, text string) {
		dpos := 0
		gr := uniseg.NewGraphemes(text)
		for gr.Next() {
			cl := gr.Str()
			w := runewidth.StringWidth(cl)
			if w < 1 {
				rns := []rune(cl)
				if len(rns) == 0 {
					continue
				}
				w = runewidth.RuneWidth(rns[0])
			}
			if w < 1 {
				continue
			}
			col0 := vx + dpos - t.HOffset
			if col0 >= contentW {
				break
			}
			if col0+w <= 0 {
				dpos += w
				continue
			}
			rns := []rune(cl)
			if len(rns) == 0 {
				dpos += w
				continue
			}
			cx := r.X + imax(0, col0)
			scr.SetContent(cx, y, rns[0], rns[1:], style)
			dpos += w
		}
	}

	// Header row
	vx := 0
	for i, h := range t.Headers {
		if i > 0 {
			vx += t.ColGaps
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
			text = runewidth.FillLeft(label, widths[i])
		} else {
			text = runewidth.FillRight(label, widths[i])
		}
		drawShifted(vx, r.Y, StyleHeader, text)
		vx += widths[i]
	}

	// Data rows
	for vi := range vis {
		di := t.Offset + vi
		if di >= len(t.Rows) {
			break
		}
		row := t.Rows[di]
		y := r.Y + 1 + vi
		isSel := t.Active && di == t.SelectedRow

		if isSel {
			fillRow(scr, r.X, y, contentW, StyleSelected)
		}

		vx := 0
		for i := range len(t.Headers) {
			if i > 0 {
				vx += t.ColGaps
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
				text = runewidth.FillLeft(cell.Text, widths[i])
			} else {
				text = runewidth.FillRight(cell.Text, widths[i])
			}
			drawShifted(vx, y, style, text)
			vx += widths[i]
		}
	}

	// Left and right overflow hint arrows render on the header row so
	// the user can tell more columns exist off screen.
	if t.HOffset > 0 {
		scr.SetContent(r.X, r.Y, '◂', nil, StyleDefault.Foreground(ColorAccent).Bold(true))
	}
	if totalW-t.HOffset > contentW {
		scr.SetContent(r.X+contentW-1, r.Y, '▸', nil, StyleDefault.Foreground(ColorAccent).Bold(true))
	}

	if needsBar {
		t.ScrollbarRect = Rect{X: r.X + r.W - 1, Y: r.Y + 1, W: 1, H: vis}
		drawScrollbar(scr, t.ScrollbarRect.X, t.ScrollbarRect.Y,
			t.ScrollbarRect.H, vis, len(t.Rows), t.Offset)
	} else {
		t.ScrollbarRect = Rect{}
	}
}

// JumpToScrollbarY maps an absolute screen Y inside the scrollbar track to a
// row offset. Used by the App to implement click-to-jump and drag.
func (t *TableWidget) JumpToScrollbarY(y int) {
	if t.ScrollbarRect.H <= 0 {
		return
	}
	rel := max(y-t.ScrollbarRect.Y, 0)
	if rel >= t.ScrollbarRect.H {
		rel = t.ScrollbarRect.H - 1
	}
	vis := t.ScrollbarRect.H
	total := len(t.Rows)
	maxOff := imax(0, total-vis)
	// Map the click row across the track to a proportional offset.
	newOff := rel * maxOff / imax(1, t.ScrollbarRect.H-1)
	t.Offset = clamp(newOff, 0, maxOff)
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
	if !t.Active {
		t.Active = true
		t.SelectedRow = clamp(t.SelectedRow, 0, len(t.Rows)-1)
		t.ensureVisible()
		if t.OnSelect != nil {
			t.OnSelect(t.SelectedRow)
		}
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
	if !t.Active {
		t.Active = true
		t.SelectedRow = clamp(t.SelectedRow, 0, len(t.Rows)-1)
		t.ensureVisible()
		if t.OnSelect != nil {
			t.OnSelect(t.SelectedRow)
		}
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

// ScrollLeft nudges horizontal offset by n cells, clamping at zero.
func (t *TableWidget) ScrollLeft(n int) {
	t.HOffset = imax(0, t.HOffset-n)
}

// ScrollRight nudges horizontal offset by n cells, clamping at the
// rightmost position where the widest column is still partly visible.
func (t *TableWidget) ScrollRight(n int) {
	maxOff := imax(0, t.LastContentWidth-t.Rect.W+1)
	t.HOffset = clamp(t.HOffset+n, 0, maxOff)
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
	case tcell.KeyLeft:
		t.ScrollLeft(6)
		return true
	case tcell.KeyRight:
		t.ScrollRight(6)
		return true
	case tcell.KeyEnter, tcell.KeyLF:
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
		case 'h':
			t.ScrollLeft(6)
			return true
		case 'l':
			t.ScrollRight(6)
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
