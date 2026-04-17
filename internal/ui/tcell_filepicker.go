package ui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// FilePickerEntry is one row in the file picker. Directories sort
// before files; the parent ".." is always at index 0 unless we are at
// the root.
type FilePickerEntry struct {
	Name string
	Dir  bool
}

// FilePickerOverlay is a directory navigator. The user moves the cursor
// with arrow keys (or j/k), descends into a directory with Enter, and
// commits the current directory with "s" (select). Esc cancels.
type FilePickerOverlay struct {
	Title    string
	cwd      string
	entries  []FilePickerEntry
	cursor   int
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
	p.cursor = 0
	p.offset = 0
}

func (p *FilePickerOverlay) Draw(scr tcell.Screen, r Rect) {
	w := 70
	if w > r.W-4 {
		w = r.W - 4
	}
	h := 22
	if h > r.H-2 {
		h = r.H - 2
	}
	box := Rect{X: r.X + (r.W-w)/2, Y: r.Y + (r.H-h)/2, W: w, H: h}
	p.rect = box

	clearRect(scr, box)
	drawBoxBorder(scr, box, ColorAccent)

	if p.Title != "" {
		drawString(scr, box.X+2, box.Y, StyleDefault.Foreground(ColorAccent).Bold(true), " "+p.Title+" ", box.W-4)
	}
	drawString(scr, box.X+2, box.Y+1, StyleSubtext.Bold(true), "📁 "+p.cwd, box.W-4)

	listTop := box.Y + 3
	listH := box.H - 5
	maxRow := listTop + listH
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+listH {
		p.offset = p.cursor - listH + 1
	}

	y := listTop
	for i := p.offset; i < len(p.entries) && y < maxRow; i++ {
		e := p.entries[i]
		marker := "  "
		style := StyleDefault.Foreground(ColorText)
		if e.Dir {
			style = StyleDefault.Foreground(ColorAccent)
		}
		if i == p.cursor {
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

	hint := "  ↑↓ move · enter open · s select cwd · esc cancel"
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
			row := y - (p.rect.Y + 3) + p.offset
			if row >= 0 && row < len(p.entries) {
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
		return true
	case tcell.KeyEnd:
		p.cursor = len(p.entries) - 1
		return true
	case tcell.KeyEnter:
		p.activate()
		return true
	case tcell.KeyRune:
		switch e.Rune() {
		case 'j':
			p.move(+1)
		case 'k':
			p.move(-1)
		case 'q':
			if p.OnCancel != nil {
				p.OnCancel()
			}
		case 's', 'S':
			if p.OnSelect != nil {
				p.OnSelect(p.cwd)
			}
		case 'h':
			// Step out one directory.
			parent := filepath.Dir(p.cwd)
			if parent != p.cwd {
				p.changeDir(parent)
			}
		case 'l':
			// Step into the highlighted directory if it is one.
			p.activate()
		}
		return true
	}
	return false
}

func (p *FilePickerOverlay) move(delta int) {
	if len(p.entries) == 0 {
		return
	}
	p.cursor += delta
	if p.cursor < 0 {
		p.cursor = 0
	}
	if p.cursor >= len(p.entries) {
		p.cursor = len(p.entries) - 1
	}
}

func (p *FilePickerOverlay) activate() {
	if p.cursor < 0 || p.cursor >= len(p.entries) {
		return
	}
	e := p.entries[p.cursor]
	if !e.Dir {
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
