package ui

import "testing"

func TestTableMoveDownFirstPressActivatesFirstRow(t *testing.T) {
	table := NewTableWidget([]string{"NAME"})
	table.Rows = [][]TableCell{
		{{Text: "a"}},
		{{Text: "b"}},
		{{Text: "c"}},
	}
	table.SelectedRow = 0
	table.Active = false
	table.Rect = Rect{X: 0, Y: 0, W: 40, H: 6}

	table.MoveDown(1)
	if !table.Active {
		t.Fatalf("expected first move-down to activate table")
	}
	if table.SelectedRow != 0 {
		t.Fatalf("expected first move-down to select first row, got %d", table.SelectedRow)
	}

	table.MoveDown(1)
	if table.SelectedRow != 1 {
		t.Fatalf("expected second move-down to select second row, got %d", table.SelectedRow)
	}
}

func TestTableMoveUpFirstPressActivatesCurrentRow(t *testing.T) {
	table := NewTableWidget([]string{"NAME"})
	table.Rows = [][]TableCell{
		{{Text: "a"}},
		{{Text: "b"}},
		{{Text: "c"}},
	}
	table.SelectedRow = 2
	table.Active = false
	table.Rect = Rect{X: 0, Y: 0, W: 40, H: 6}

	table.MoveUp(1)
	if !table.Active {
		t.Fatalf("expected first move-up to activate table")
	}
	if table.SelectedRow != 2 {
		t.Fatalf("expected first move-up to keep current row selected, got %d", table.SelectedRow)
	}

	table.MoveUp(1)
	if table.SelectedRow != 1 {
		t.Fatalf("expected second move-up to move selection, got %d", table.SelectedRow)
	}
}
