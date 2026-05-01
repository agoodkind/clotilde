package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/sahilm/fuzzy"
)

// FilePickerEntry is one row in the file picker. Directories sort
// before files; the parent ".." is always at index 0 unless we are at
// the root.
type FilePickerEntry struct {
	Name string
	Dir  bool
}

// FilePickerOverlay is a directory navigator with an inline input
// bar. The input doubles as a fuzzy filter and a path entry.
//
// Typing plain characters narrows the visible entries by
// case insensitive substring match. Typing a forward slash interprets
// the buffered text as a directory name and navigates into it (or, if
// the text starts with a slash, navigates to the absolute path).
// Backspace edits the buffer. Up/Down move the cursor. Enter on a
// directory descends; Enter on a file or on an empty list commits
// the current cwd. The "s" key always commits the cwd as the
// selection. Esc clears the buffer first, then cancels on second press.
type FilePickerOverlay struct {
	Title    string
	cwd      string
	entries  []FilePickerEntry // current directory contents (unfiltered)
	visible  []int             // indices into entries that pass the filter
	filter   string            // current input buffer (filter or path fragment)
	cursor   int               // index into visible
	offset   int
	rect     Rect
	OnSelect func(path string)
	OnCancel func()
}

// NewFilePickerOverlay opens the picker rooted at start. If start is
// empty the user's home directory is used as a fallback.
func NewFilePickerOverlay(title, start string) *FilePickerOverlay {
	if start == "" {
		start, _ = os.UserHomeDir()
	}
	if start == "" {
		start = "/"
	}
	p := &FilePickerOverlay{Title: title}
	p.changeDir(start)
	return p
}

// changeDir replaces the current directory and rebuilds the entry
// list. Directories render before files; both are sorted alphabetically.
func (p *FilePickerOverlay) changeDir(dir string) {
	clean := filepath.Clean(dir)
	if info, err := os.Stat(clean); err != nil || !info.IsDir() {
		return
	}
	p.cwd = clean
	p.entries = p.entries[:0]
	if clean != "/" {
		p.entries = append(p.entries, FilePickerEntry{Name: "..", Dir: true})
	}
	items, err := os.ReadDir(clean)
	if err == nil {
		var dirs, files []FilePickerEntry
		for _, it := range items {
			name := it.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			if it.IsDir() {
				dirs = append(dirs, FilePickerEntry{Name: name, Dir: true})
				continue
			}
			files = append(files, FilePickerEntry{Name: name, Dir: false})
		}
		sort.Slice(dirs, func(i, j int) bool { return dirs[i].Name < dirs[j].Name })
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
		p.entries = append(p.entries, dirs...)
		p.entries = append(p.entries, files...)
	}
	p.filter = ""
	p.recomputeVisible()
}

// recomputeVisible rebuilds the visible index slice based on the
// current filter buffer. Empty filter shows everything in directory
// order. With a filter, sahilm/fuzzy ranks entries by match quality
// so the best candidate sits at the top. The cursor clamps to the
// new range.
func (p *FilePickerOverlay) recomputeVisible() {
	p.visible = p.visible[:0]
	if p.filter == "" {
		for i := range p.entries {
			p.visible = append(p.visible, i)
		}
	} else {
		names := make([]string, len(p.entries))
		for i, e := range p.entries {
			names[i] = e.Name
		}
		matches := fuzzy.Find(p.filter, names)
		for _, m := range matches {
			p.visible = append(p.visible, m.Index)
		}
	}
	if p.cursor >= len(p.visible) {
		p.cursor = imax(0, len(p.visible)-1)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
	p.syncOffsetForCursor()
}

// filePickerViewportH is the default list height when syncing scroll from
// key handlers before Draw runs with the real box size.
const filePickerViewportH = 16

func (p *FilePickerOverlay) syncOffsetForCursor() {
	p.syncOffsetForCursorWithHeight(filePickerViewportH)
}

func (p *FilePickerOverlay) syncOffsetForCursorWithHeight(listH int) {
	if len(p.visible) == 0 {
		p.offset = 0
		return
	}
	if listH < 1 {
		listH = 1
	}
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+listH {
		p.offset = p.cursor - listH + 1
	}
	if p.offset < 0 {
		p.offset = 0
	}
	if len(p.visible) <= listH {
		p.offset = 0
	}
}

func (p *FilePickerOverlay) Draw(scr tcell.Screen, r Rect) {
	w := min(70, r.W-4)
	h := min(22, r.H-2)
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	p.rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorAccent)

	if p.Title != "" {
		drawString(scr, box.X+2, box.Y, StyleDefault.Foreground(ColorAccent).Bold(true), " "+p.Title+" ", box.W-4)
	}
	drawString(scr, box.X+2, box.Y+1, StyleSubtext.Bold(true), "cwd: "+p.cwd, box.W-4)

	// Input bar. The underline shows the editable region; cursor mark
	// lives at the end of the buffer so the user sees the typing point.
	inputLabel := "› "
	drawString(scr, box.X+2, box.Y+2, StyleMuted, inputLabel, box.W-4)
	inputStyle := StyleDefault.Foreground(ColorText).Bold(true)
	drawString(scr, box.X+2+runeCount(inputLabel), box.Y+2, inputStyle, p.filter+"_", box.W-4-runeCount(inputLabel))

	listTop := box.Y + 4
	listH := box.H - 6
	maxRow := listTop + listH
	p.syncOffsetForCursorWithHeight(listH)

	y := listTop
	for vi := p.offset; vi < len(p.visible) && y < maxRow; vi++ {
		idx := p.visible[vi]
		e := p.entries[idx]
		marker := "  "
		style := StyleDefault.Foreground(ColorText)
		if e.Dir {
			style = StyleDefault.Foreground(ColorAccent)
		}
		if vi == p.cursor {
			marker = "▸ "
			style = style.Bold(true).Reverse(true)
		}
		label := e.Name
		if e.Dir {
			label += "/"
		}
		drawString(scr, box.X+2, y, style, marker+label, box.W-4)
		y++
	}
	if len(p.visible) == 0 {
		hint := "(no matches; press / to descend, s to commit current cwd)"
		drawString(scr, box.X+2, listTop, StyleMuted, hint, box.W-4)
	}

	hint := "  type to filter · / descend · enter select/open · s commit cwd · esc back"
	drawString(scr, box.X+2, box.Y+box.H-1, StyleMuted, hint, box.W-4)
}

func (p *FilePickerOverlay) HandleEvent(ev tcell.Event) bool {
	switch e := ev.(type) {
	case *tcell.EventKey:
		return p.handleKey(e)
	case *tcell.EventMouse:
		x, y := e.Position()
		if !p.rect.Contains(x, y) {
			if e.Buttons() != 0 && p.OnCancel != nil {
				p.OnCancel()
			}
			return true
		}
		if e.Buttons()&tcell.ButtonPrimary != 0 {
			row := y - (p.rect.Y + 4) + p.offset
			if row >= 0 && row < len(p.visible) {
				p.cursor = row
				p.activate()
			}
		}
		return true
	}
	return false
}

func (p *FilePickerOverlay) handleKey(e *tcell.EventKey) bool {
	switch e.Key() {
	case tcell.KeyEscape:
		if p.filter != "" {
			p.filter = ""
			p.recomputeVisible()
			return true
		}
		if p.OnCancel != nil {
			p.OnCancel()
		}
		return true
	case tcell.KeyUp:
		p.move(-1)
		return true
	case tcell.KeyDown:
		p.move(+1)
		return true
	case tcell.KeyPgUp:
		p.move(-10)
		return true
	case tcell.KeyPgDn:
		p.move(+10)
		return true
	case tcell.KeyHome:
		p.cursor = 0
		p.syncOffsetForCursor()
		return true
	case tcell.KeyEnd:
		p.cursor = imax(0, len(p.visible)-1)
		p.syncOffsetForCursor()
		return true
	case tcell.KeyEnter, tcell.KeyLF:
		p.activate()
		return true
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		if rs := []rune(p.filter); len(rs) > 0 {
			p.filter = string(rs[:len(rs)-1])
			p.recomputeVisible()
		}
		return true
	case tcell.KeyTab:
		// Tab completes the filter to the highlighted entry's name so
		// the user can type a few characters and Tab to lock in.
		if p.cursor < len(p.visible) {
			p.filter = p.entries[p.visible[p.cursor]].Name
			p.recomputeVisible()
		}
		return true
	case tcell.KeyCtrlU:
		p.filter = ""
		p.recomputeVisible()
		return true
	case tcell.KeyRune:
		r := e.Rune()
		// Slash navigates. If the buffer is empty it descends into the
		// highlighted dir. If the buffer holds an absolute path it
		// jumps there. Otherwise it treats the buffer as a directory
		// name relative to cwd.
		if r == '/' {
			p.descendByInput()
			return true
		}
		// "s" with empty filter commits the cwd. With a filter typed,
		// "s" is just a normal character so the user can type folder
		// names containing s.
		if (r == 's' || r == 'S') && p.filter == "" {
			if p.OnSelect != nil {
				p.OnSelect(p.cwd)
			}
			return true
		}
		// Append the rune to the filter buffer.
		p.filter += string(r)
		p.recomputeVisible()
		return true
	}
	return false
}

// descendByInput resolves the current filter buffer as a path and
// changes directory to it. Empty buffer descends into the highlighted
// entry. Absolute paths jump anywhere. Relative names join with cwd.
func (p *FilePickerOverlay) descendByInput() {
	if p.filter == "" {
		p.activate()
		return
	}
	target := p.filter
	if !strings.HasPrefix(target, "/") {
		// Use the highlighted entry's name when the buffer is a
		// substring match for it; otherwise treat the buffer literally.
		if p.cursor < len(p.visible) {
			candidate := p.entries[p.visible[p.cursor]].Name
			if strings.Contains(strings.ToLower(candidate), strings.ToLower(target)) && p.entries[p.visible[p.cursor]].Dir {
				target = candidate
			}
		}
		target = filepath.Join(p.cwd, target)
	}
	p.changeDir(target)
}

func (p *FilePickerOverlay) move(delta int) {
	if len(p.visible) == 0 {
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.visible) {
		p.cursor = len(p.visible) - 1
	}
	p.syncOffsetForCursor()
}

func (p *FilePickerOverlay) activate() {
	if p.cursor < 0 || p.cursor >= len(p.visible) {
		// Empty list: commit the current cwd as the selection. This
		// covers the case where the user typed a filter that matches
		// nothing but still wants to select the directory.
		if p.OnSelect != nil {
			p.OnSelect(p.cwd)
		}
		return
	}
	e := p.entries[p.visible[p.cursor]]
	if !e.Dir {
		// Files are not selectable; treat Enter on a file as commit
		// of the parent directory (which is p.cwd).
		if p.OnSelect != nil {
			p.OnSelect(p.cwd)
		}
		return
	}
	if e.Name == ".." {
		parent := filepath.Dir(p.cwd)
		if parent != p.cwd {
			p.changeDir(parent)
		}
		return
	}
	p.changeDir(filepath.Join(p.cwd, e.Name))
}
