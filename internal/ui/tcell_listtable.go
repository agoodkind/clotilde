package ui

import "github.com/gdamore/tcell/v2"

// TableModel is the public handle for the cmd/list interactive table.
// Builder chain parity: NewTable(headers, rows).WithSorting()
type TableModel struct {
	Headers []string
	Rows    [][]string
	Sort    bool
}

// NewTable constructs a TableModel.
func NewTable(headers []string, rows [][]string) TableModel {
	return TableModel{Headers: headers, Rows: rows}
}

// WithSorting is a no-op kept for API compatibility. Sorting is always
// available in the tcell port via numeric keys 1-N.
func (m TableModel) WithSorting() TableModel {
	m.Sort = true
	return m
}

// RunTable displays a scrollable, selectable table. Returns the string
// cells of the chosen row, or an empty slice if the user cancels.
func RunTable(m TableModel) ([]string, error) {
	var selected []string
	err := runOverlay(func(done func()) Widget {
		tbl := NewTableWidget(m.Headers)
		rows := make([][]TableCell, 0, len(m.Rows))
		for _, r := range m.Rows {
			cells := make([]TableCell, len(r))
			for i, c := range r {
				cells[i] = TableCell{Text: c, Style: StyleDefault}
			}
			rows = append(rows, cells)
		}
		tbl.Rows = rows
		tbl.Active = len(rows) > 0
		tbl.OnActivate = func(row int) {
			if row >= 0 && row < len(m.Rows) {
				selected = m.Rows[row]
				done()
			}
		}
		return &listTable{tbl: tbl, onCancel: done}
	})
	if err != nil {
		return nil, err
	}
	return selected, nil
}

type listTable struct {
	tbl      *TableWidget
	rect     Rect
	onCancel func()
}

func (l *listTable) Draw(scr tcell.Screen, r Rect) {
	l.rect = r
	fillRow(scr, r.X, r.Y, r.W, StyleHeaderBar)
	drawString(scr, r.X+1, r.Y, StyleHeaderBar.Bold(true), " Sessions", r.W-1)
	fillRow(scr, r.X, r.Y+r.H-1, r.W, StyleStatusBar)
	drawString(scr, r.X+1, r.Y+r.H-1, StyleStatusBar,
		" ↑↓ move   enter select   q/esc cancel ", r.W-1)

	body := Rect{X: r.X + 1, Y: r.Y + 1, W: r.W - 2, H: r.H - 2}
	l.tbl.Draw(scr, body)
}

func (l *listTable) HandleEvent(ev tcell.Event) bool {
	if ek, ok := ev.(*tcell.EventKey); ok {
		if ek.Key() == tcell.KeyEscape || (ek.Key() == tcell.KeyRune && ek.Rune() == 'q') {
			if l.onCancel != nil {
				l.onCancel()
			}
			return true
		}
	}
	if em, ok := ev.(*tcell.EventMouse); ok {
		btns := em.Buttons()
		if btns&tcell.WheelUp != 0 {
			l.tbl.ScrollUp(3)
			return true
		}
		if btns&tcell.WheelDown != 0 {
			l.tbl.ScrollDown(3)
			return true
		}
		if btns&tcell.Button1 != 0 {
			x, y := em.Position()
			_ = x
			row := l.tbl.RowAtY(y)
			if row >= 0 {
				l.tbl.SelectAt(row)
			}
			return true
		}
	}
	return l.tbl.HandleEvent(ev)
}
