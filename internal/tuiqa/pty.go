package tuiqa

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

type ptyDriver struct {
	mu   sync.Mutex
	term vt10x.Terminal
	cmd  *exec.Cmd
	ptyF *os.File
}

func newPTYDriver() *ptyDriver {
	t := vt10x.New(vt10x.WithSize(120, 40))
	return &ptyDriver{
		term: t,
	}
}

func (d *ptyDriver) Name() string { return "pty" }

func (d *ptyDriver) Start(binaryPath string, env []string, cols, rows int) error {
	_ = d.Kill()
	if cols <= 0 {
		cols = 120
	}
	if rows <= 0 {
		rows = 40
	}
	d.term = vt10x.New(vt10x.WithSize(cols, rows))
	d.cmd = exec.Command(binaryPath)
	d.cmd.Env = append(os.Environ(), env...)
	ws := &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}
	pt, err := pty.StartWithSize(d.cmd, ws)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}
	d.ptyF = pt
	go d.readLoop(pt)
	return nil
}

func (d *ptyDriver) readLoop(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			d.mu.Lock()
			_, _ = d.term.Write(buf[:n])
			d.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (d *ptyDriver) Kill() error {
	if d.ptyF != nil {
		_ = d.ptyF.Close()
		d.ptyF = nil
	}
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Signal(syscall.SIGTERM)
	}
	d.cmd = nil
	return nil
}

func (d *ptyDriver) Capture() (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.term.String(), nil
}

func (d *ptyDriver) SendKey(tokens []string) error {
	var b []byte
	for _, tok := range tokens {
		b = append(b, TokenToBytes(tok)...)
	}
	return d.PasteRaw(b)
}

func (d *ptyDriver) PasteRaw(data []byte) error {
	if d.ptyF == nil {
		return fmt.Errorf("pty: not started")
	}
	_, err := d.ptyF.Write(data)
	return err
}

func (d *ptyDriver) SessionAlive() bool {
	if d.cmd == nil || d.cmd.Process == nil {
		return false
	}
	err := d.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// ParseSendTokens parses a send command tail into tmux-style tokens (split on spaces, respect quotes).
func ParseSendTokens(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"' && !inQuote:
			inQuote = true
		case r == '"' && inQuote:
			inQuote = false
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}
