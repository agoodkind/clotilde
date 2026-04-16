package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// SortColumn identifies which column the table is sorted by.
type SortColumn int

const (
	SortColName SortColumn = iota
	SortColWorkspace
	SortColModel
	SortColCreated
	SortColUsed
)

// SessionTable wraps a tview.Table with sortable headers, filtering,
// and async stats loading. It is the primary widget in the clotilde TUI.
type SessionTable struct {
	*tview.Table

	sessions []*session.Session // full unfiltered list
	filtered []*session.Session // after applying filter
	sortCol  SortColumn
	sortAsc  bool
	filter   string

	// Stats cache: transcript path -> computed stats (populated async)
	StatsCache map[string]*transcript.CompactQuickStats

	// Model cache: session name -> model string (populated by caller async)
	ModelCache map[string]string

	// Callbacks
	OnSelect func(sess *session.Session) // called when a row is selected (Enter/click)
	OnResume func(sess *session.Session) // called on double-click

	// Column headers
	headers []string
}

// NewSessionTable creates a new session table widget.
func NewSessionTable() *SessionTable {
	t := &SessionTable{
		Table:      tview.NewTable(),
		headers:    []string{"NAME", "WORKSPACE", "MODEL", "CREATED", "LAST USED"},
		sortCol:    SortColUsed,
		sortAsc:    false, // most recent first
		StatsCache: make(map[string]*transcript.CompactQuickStats),
		ModelCache: make(map[string]string),
	}

	t.Table.
		SetBorders(false).
		SetSelectable(true, false). // rows selectable, not columns
		SetFixed(1, 0).             // fix header row
		SetSeparator(' ')

	t.Table.SetSelectedFunc(func(row, col int) {
		if row < 1 { // header row
			return
		}
		idx := row - 1
		if idx < len(t.filtered) && t.OnSelect != nil {
			t.OnSelect(t.filtered[idx])
		}
	})

	// Handle mouse clicks on headers for sorting
	t.Table.SetMouseCapture(func(action tview.MouseAction, event *tcell.EventMouse) (tview.MouseAction, *tcell.EventMouse) {
		if action == tview.MouseLeftClick {
			_, y := event.Position()
			if y == 0 { // header row
				x, _ := event.Position()
				col := t.columnAtX(x)
				if col >= 0 {
					t.ToggleSort(SortColumn(col))
				}
				return action, nil
			}
		}
		return action, event
	})

	return t
}

// SetSessions sets the full session list and re-renders.
func (t *SessionTable) SetSessions(sessions []*session.Session) {
	t.sessions = sessions
	t.applyFilterAndSort()
	t.render()
}

// SetFilter applies a text filter to the session list.
func (t *SessionTable) SetFilter(filter string) {
	t.filter = filter
	t.applyFilterAndSort()
	t.render()
}

// ToggleSort changes the sort column. If already sorted by this column, reverses direction.
func (t *SessionTable) ToggleSort(col SortColumn) {
	if t.sortCol == col {
		t.sortAsc = !t.sortAsc
	} else {
		t.sortCol = col
		t.sortAsc = col == SortColName // name sorts A-Z by default, others descending
	}
	t.applyFilterAndSort()
	t.render()
}

// SelectedSession returns the currently highlighted session, or nil.
func (t *SessionTable) SelectedSession() *session.Session {
	row, _ := t.Table.GetSelection()
	if row < 1 || row-1 >= len(t.filtered) {
		return nil
	}
	return t.filtered[row-1]
}

// applyFilterAndSort filters then sorts the session list.
func (t *SessionTable) applyFilterAndSort() {
	// Filter
	if t.filter == "" {
		t.filtered = make([]*session.Session, len(t.sessions))
		copy(t.filtered, t.sessions)
	} else {
		t.filtered = nil
		lf := strings.ToLower(t.filter)
		for _, s := range t.sessions {
			if strings.Contains(strings.ToLower(s.Name), lf) ||
				strings.Contains(strings.ToLower(s.Metadata.WorkspaceRoot), lf) ||
				strings.Contains(strings.ToLower(s.Metadata.Context), lf) {
				t.filtered = append(t.filtered, s)
			}
		}
	}

	// Sort
	sort.SliceStable(t.filtered, func(i, j int) bool {
		a, b := t.filtered[i], t.filtered[j]
		var less bool
		switch t.sortCol {
		case SortColName:
			less = strings.ToLower(a.Name) < strings.ToLower(b.Name)
		case SortColWorkspace:
			less = a.Metadata.WorkspaceRoot < b.Metadata.WorkspaceRoot
		case SortColModel:
			// model not stored in metadata directly; sort by name as fallback
			less = a.Name < b.Name
		case SortColCreated:
			less = a.Metadata.Created.Before(b.Metadata.Created)
		case SortColUsed:
			less = a.Metadata.LastAccessed.Before(b.Metadata.LastAccessed)
		}
		if !t.sortAsc {
			less = !less
		}
		return less
	})
}

// render rebuilds the table content from filtered sessions.
func (t *SessionTable) render() {
	t.Table.Clear()

	// Header row with background
	headerBg := tcell.ColorDarkSlateGray
	for col, h := range t.headers {
		indicator := ""
		if SortColumn(col) == t.sortCol {
			if t.sortAsc {
				indicator = " ^"
			} else {
				indicator = " v"
			}
		}
		cell := tview.NewTableCell(" " + h + indicator + " ").
			SetSelectable(false).
			SetTextColor(tcell.ColorWhite).
			SetBackgroundColor(headerBg).
			SetAttributes(tcell.AttrBold).
			SetExpansion(1)
		t.Table.SetCell(0, col, cell)
	}

	// Data rows with alternating background
	evenBg := tcell.ColorDefault
	oddBg := tcell.NewRGBColor(30, 30, 40) // subtle dark alternate

	for i, sess := range t.filtered {
		row := i + 1
		bg := evenBg
		if i%2 == 1 {
			bg = oddBg
		}

		// Name (with context as subtitle if present)
		name := sess.Name
		if len(name) > 35 {
			name = name[:32] + "..."
		}
		nameColor := tcell.ColorWhite
		if sess.Metadata.IsForkedSession {
			nameColor = tcell.ColorYellow
		} else if sess.Metadata.IsIncognito {
			nameColor = tcell.ColorPurple
		}
		nameCell := tview.NewTableCell(name).
			SetTextColor(nameColor).
			SetBackgroundColor(bg).
			SetExpansion(2)

		// Workspace
		ws := shortPath(sess.Metadata.WorkspaceRoot)
		if len(ws) > 22 {
			ws = "..." + ws[len(ws)-19:]
		}
		wsCell := tview.NewTableCell(ws).
			SetTextColor(tcell.Color246).
			SetBackgroundColor(bg).
			SetExpansion(1)

		// Model from cache
		model := "-"
		if m, ok := t.ModelCache[sess.Name]; ok && m != "" {
			model = m
		}
		modelColor := tcell.Color246
		switch {
		case strings.Contains(model, "opus"):
			modelColor = tcell.ColorGold
		case strings.Contains(model, "sonnet"):
			modelColor = tcell.ColorCornflowerBlue
		case strings.Contains(model, "haiku"):
			modelColor = tcell.ColorLightGreen
		}
		modelCell := tview.NewTableCell(model).
			SetTextColor(modelColor).
			SetBackgroundColor(bg).
			SetExpansion(1)

		// Created
		created := sess.Metadata.Created.Format("Jan 02")
		createdCell := tview.NewTableCell(created).
			SetTextColor(tcell.Color246).
			SetBackgroundColor(bg).
			SetExpansion(1)

		// Last used
		lastUsed := util.FormatRelativeTime(sess.Metadata.LastAccessed)
		usedCell := tview.NewTableCell(lastUsed).
			SetTextColor(tcell.Color246).
			SetBackgroundColor(bg).
			SetExpansion(1)

		t.Table.SetCell(row, 0, nameCell)
		t.Table.SetCell(row, 1, wsCell)
		t.Table.SetCell(row, 2, modelCell)
		t.Table.SetCell(row, 3, createdCell)
		t.Table.SetCell(row, 4, usedCell)
	}
}

// columnAtX estimates which column a given X coordinate falls in.
// This is approximate since tview doesn't expose column boundaries directly.
func (t *SessionTable) columnAtX(x int) int {
	// Simple heuristic: divide width into equal parts
	_, _, w, _ := t.Table.GetInnerRect()
	if w <= 0 {
		return -1
	}
	colWidth := w / len(t.headers)
	col := x / colWidth
	if col >= len(t.headers) {
		col = len(t.headers) - 1
	}
	return col
}

// shortPath abbreviates a workspace path for display.
func shortPath(root string) string {
	if root == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Base(root)
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}

// FilteredCount returns the number of sessions after filtering.
func (t *SessionTable) FilteredCount() int {
	return len(t.filtered)
}

// TotalCount returns the total number of sessions before filtering.
func (t *SessionTable) TotalCount() int {
	return len(t.sessions)
}

// FormatSessionCount returns a string like "12/32 sessions" or "32 sessions".
func (t *SessionTable) FormatSessionCount() string {
	if t.filter != "" {
		return fmt.Sprintf("%d/%d sessions", len(t.filtered), len(t.sessions))
	}
	return fmt.Sprintf("%d sessions", len(t.sessions))
}
