package ui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gdamore/tcell/v2"

	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// PreviewFunc generates preview text for a session in the picker pane.
// Kept at the package level so cmd/list.richPreviewFunc can satisfy it.
type PreviewFunc func(sess *session.Session) string

// PickerModel is the public handle for the session picker.
//
// It intentionally keeps the old BubbleTea-era field names and the builder
// chain (NewPicker(...).WithPreview()) so callers in cmd/ don't need to
// change shape. Behind the scenes it now drives a small tcell screen.
type PickerModel struct {
	Sessions    []*session.Session
	Title       string
	ShowPreview bool
	PreviewFn   PreviewFunc

	// StatsCache mirrors the old field so PreviewFn implementations that
	// read it for instant stats continue to work.
	StatsCache map[string]*transcript.CompactQuickStats

	// Selected and Cancelled are filled by RunPicker on exit.
	Selected  *session.Session
	Cancelled bool
}

// NewPicker constructs a PickerModel with no preview pane.
func NewPicker(sessions []*session.Session, title string) PickerModel {
	return PickerModel{
		Sessions:   sessions,
		Title:      title,
		StatsCache: make(map[string]*transcript.CompactQuickStats),
	}
}

// WithPreview enables the side preview pane.
func (m PickerModel) WithPreview() PickerModel {
	m.ShowPreview = true
	return m
}

// RunPicker displays the session picker and returns the selected session,
// or nil when the user cancels.
func RunPicker(m PickerModel) (*session.Session, error) {
	if len(m.Sessions) == 0 {
		return nil, nil
	}
	if m.StatsCache == nil {
		m.StatsCache = make(map[string]*transcript.CompactQuickStats)
	}

	view := newPickerView(&m)
	err := runOverlay(func(done func()) Widget {
		view.OnPick = func(sess *session.Session) {
			m.Selected = sess
			done()
		}
		view.OnCancel = func() {
			m.Cancelled = true
			done()
		}
		return view
	})
	if err != nil {
		return nil, err
	}
	if m.Cancelled {
		return nil, nil
	}
	return m.Selected, nil
}

// ---------------- Internal view widget ----------------

// pickerView composes a TableWidget with an optional TextBox preview pane.
type pickerView struct {
	model   *PickerModel
	table   *TableWidget
	preview *TextBox

	// Layout last-drawn for mouse hit-testing.
	rect        Rect
	tableRect   Rect
	previewRect Rect

	// focus: 0 = table, 1 = preview
	focus int

	OnPick   func(*session.Session)
	OnCancel func()
}

func newPickerView(m *PickerModel) *pickerView {
	v := &pickerView{
		model: m,
		table: NewTableWidget([]string{"NAME", "WORKSPACE", "CREATED", "LAST USED"}),
		preview: &TextBox{
			Wrap:       true,
			TitleStyle: StyleMuted,
		},
	}
	v.populate()
	v.table.Active = true
	v.table.SelectedRow = 0
	v.table.OnActivate = func(row int) {
		if row >= 0 && row < len(m.Sessions) && v.OnPick != nil {
			v.OnPick(m.Sessions[row])
		}
	}
	v.table.OnSelect = func(row int) {
		v.updatePreview()
	}
	v.updatePreview()
	return v
}

func (v *pickerView) populate() {
	rows := make([][]TableCell, 0, len(v.model.Sessions))
	for _, s := range v.model.Sessions {
		name := s.Name
		nameStyle := StyleDefault.Foreground(ColorText)
		if s.Metadata.IsForkedSession {
			nameStyle = StyleDefault.Foreground(ColorFork)
		}
		ws := shortenPath(s.Metadata.WorkspaceRoot)
		rows = append(rows, []TableCell{
			{Text: name, Style: nameStyle},
			{Text: ws, Style: StyleSubtext},
			{Text: s.Metadata.Created.Format("Jan 02"), Style: StyleSubtext},
			{Text: util.FormatRelativeTime(s.Metadata.LastAccessed), Style: StyleSubtext},
		})
	}
	v.table.Rows = rows
}

func (v *pickerView) updatePreview() {
	if !v.model.ShowPreview {
		return
	}
	idx := v.table.SelectedRow
	if idx < 0 || idx >= len(v.model.Sessions) {
		v.preview.SetLines(nil)
		return
	}
	sess := v.model.Sessions[idx]
	v.preview.Title = " " + sess.Name + " "

	var lines []string
	if v.model.PreviewFn != nil {
		text := v.model.PreviewFn(sess)
		for _, ln := range strings.Split(text, "\n") {
			lines = append(lines, stripMarkup(ln))
		}
	} else {
		lines = defaultPickerPreview(sess, v.model.StatsCache)
	}
	v.preview.SetLines(lines)
}

// Draw paints the picker. Left column is the table; right column (if
// ShowPreview) is the preview pane.
func (v *pickerView) Draw(scr tcell.Screen, r Rect) {
	v.rect = r

	// Title bar
	title := v.model.Title
	if title == "" {
		title = "Select session"
	}
	fillRow(scr, r.X, r.Y, r.W, StyleHeaderBar)
	drawString(scr, r.X+1, r.Y, StyleHeaderBar.Bold(true), " "+title, r.W-1)

	// Footer
	footerY := r.Y + r.H - 1
	fillRow(scr, r.X, footerY, r.W, StyleStatusBar)
	var hint string
	if v.model.ShowPreview {
		hint = " ↑↓ move   enter select   tab scroll preview   q/esc cancel "
	} else {
		hint = " ↑↓ move   enter select   q/esc cancel "
	}
	drawString(scr, r.X+1, footerY, StyleStatusBar, hint, r.W-1)

	body := Rect{X: r.X, Y: r.Y + 1, W: r.W, H: r.H - 2}
	if body.H < 1 {
		return
	}

	if v.model.ShowPreview && body.W >= 60 {
		leftW := body.W * 55 / 100
		if leftW < 36 {
			leftW = 36
		}
		rightX := body.X + leftW + 1
		rightW := body.W - leftW - 1
		v.tableRect = Rect{X: body.X + 1, Y: body.Y, W: leftW - 1, H: body.H}
		v.previewRect = Rect{X: rightX, Y: body.Y, W: rightW, H: body.H}

		v.table.Draw(scr, v.tableRect)
		// Column separator
		divStyle := StyleDefault.Foreground(ColorBorder)
		for y := body.Y; y < body.Y+body.H; y++ {
			scr.SetContent(body.X+leftW, y, '│', nil, divStyle)
		}
		v.preview.Draw(scr, v.previewRect)
	} else {
		v.tableRect = Rect{X: body.X + 1, Y: body.Y, W: body.W - 2, H: body.H}
		v.table.Draw(scr, v.tableRect)
	}
}

// HandleEvent routes input. Tab swaps focus between the list and preview.
func (v *pickerView) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		switch e.Key() {
		case tcell.KeyEscape:
			if v.OnCancel != nil {
				v.OnCancel()
			}
			return true
		case tcell.KeyTab:
			if v.model.ShowPreview {
				v.focus = 1 - v.focus
				v.preview.Focused = v.focus == 1
				return true
			}
		case tcell.KeyEnter:
			row, _ := v.table.SelectedRow, 0
			if row >= 0 && row < len(v.model.Sessions) && v.OnPick != nil {
				v.OnPick(v.model.Sessions[row])
			}
			return true
		case tcell.KeyRune:
			if e.Rune() == 'q' {
				if v.OnCancel != nil {
					v.OnCancel()
				}
				return true
			}
		}
		// Forward to focused pane
		if v.focus == 1 && v.preview != nil {
			if v.preview.HandleEvent(ev) {
				return true
			}
		}
		if v.table.HandleEvent(ev) {
			v.updatePreview()
			return true
		}
	case *tcell.EventMouse:
		x, y := e.Position()
		if v.tableRect.Contains(x, y) {
			btns := e.Buttons()
			if btns&tcell.WheelUp != 0 {
				v.table.ScrollUp(3)
				return true
			}
			if btns&tcell.WheelDown != 0 {
				v.table.ScrollDown(3)
				return true
			}
			if btns&tcell.Button1 != 0 {
				row := v.table.RowAtY(y)
				if row >= 0 {
					v.table.SelectAt(row)
					v.updatePreview()
				}
				return true
			}
		}
		if v.model.ShowPreview && v.previewRect.Contains(x, y) {
			btns := e.Buttons()
			if btns&tcell.WheelUp != 0 {
				v.preview.Offset = imax(0, v.preview.Offset-3)
				return true
			}
			if btns&tcell.WheelDown != 0 {
				v.preview.Offset += 3
				return true
			}
		}
	}
	return false
}

// defaultPickerPreview builds the standard preview when no PreviewFn is
// supplied: a short identity block plus any cached stats.
func defaultPickerPreview(sess *session.Session, statsCache map[string]*transcript.CompactQuickStats) []string {
	var b []string
	b = append(b, sess.Name)
	if sess.Metadata.Context != "" {
		b = append(b, sess.Metadata.Context)
	}
	b = append(b, "")
	b = append(b, fmt.Sprintf("Workspace:  %s", shortenPath(sess.Metadata.WorkspaceRoot)))
	if sess.Metadata.IsForkedSession {
		b = append(b, fmt.Sprintf("Fork of:    %s", sess.Metadata.ParentSession))
	}
	b = append(b, fmt.Sprintf("Created:    %s", sess.Metadata.Created.Format("2006-01-02 15:04")))
	b = append(b, fmt.Sprintf("Last used:  %s", util.FormatRelativeTime(sess.Metadata.LastAccessed)))
	if sess.Metadata.TranscriptPath != "" {
		if info, err := os.Stat(sess.Metadata.TranscriptPath); err == nil {
			b = append(b, fmt.Sprintf("Transcript: %.1f MB", float64(info.Size())/(1024*1024)))
		}
	}
	if qs, ok := statsCache[sess.Metadata.TranscriptPath]; ok {
		b = append(b, "")
		b = append(b, fmt.Sprintf("Tokens:     ~%s", fmtTokens(qs.EstimatedTokens)))
		b = append(b, fmt.Sprintf("Entries:    %d", qs.TotalEntries))
		b = append(b, fmt.Sprintf("Compactions: %d", qs.Compactions))
	}
	b = append(b, "")
	b = append(b, "UUID: "+sess.Metadata.SessionID)
	return b
}

func shortenPath(p string) string {
	if p == "" {
		return "-"
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		if p == home {
			return "~"
		}
		if strings.HasPrefix(p, home+"/") {
			return "~/" + p[len(home)+1:]
		}
	}
	return filepath.Clean(p)
}
