package tuiqa

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

type tmuxDriver struct {
	session string
	cols    int
	rows    int
}

func newTmuxDriver() *tmuxDriver {
	pid := os.Getpid()
	return &tmuxDriver{
		session: fmt.Sprintf("clyde-tuiqa-tmux-%d", pid),
		cols:    120,
		rows:    40,
	}
}

// SetSessionName overrides the default tmux session name (for multi-step agent runs).
func (d *tmuxDriver) SetSessionName(name string) {
	if name != "" {
		d.session = name
	}
}

func (d *tmuxDriver) Name() string { return "tmux" }

func (d *tmuxDriver) sh(args []string, stdin []byte) (string, error) {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(stdin) > 0 {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := cmd.Run()
	out := stdout.String()
	if err != nil {
		return out, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func (d *tmuxDriver) Start(binaryPath string, env []string, cols, rows int) error {
	d.cols = cols
	d.rows = rows
	if d.cols <= 0 {
		d.cols = 120
	}
	if d.rows <= 0 {
		d.rows = 40
	}
	_ = d.Kill()
	args := []string{
		"tmux", "new-session", "-d", "-s", d.session,
		"-x", strconv.Itoa(d.cols), "-y", strconv.Itoa(d.rows),
	}
	// Run: env KEY=val ... /binary
	cmdParts := []string{"env"}
	cmdParts = append(cmdParts, env...)
	cmdParts = append(cmdParts, binaryPath)
	args = append(args, cmdParts...)
	_, err := d.sh(args, nil)
	return err
}

func (d *tmuxDriver) Kill() error {
	_, _ = d.sh([]string{"tmux", "kill-session", "-t", d.session}, nil)
	return nil
}

func (d *tmuxDriver) Capture() (string, error) {
	return d.sh([]string{"tmux", "capture-pane", "-t", d.session, "-p"}, nil)
}

func (d *tmuxDriver) SendKey(tokens []string) error {
	args := []string{"tmux", "send-keys", "-t", d.session}
	args = append(args, tokens...)
	_, err := d.sh(args, nil)
	return err
}

func (d *tmuxDriver) PasteRaw(b []byte) error {
	_, err := d.sh([]string{"tmux", "load-buffer", "-"}, b)
	if err != nil {
		return err
	}
	_, err = d.sh([]string{"tmux", "paste-buffer", "-t", d.session}, nil)
	return err
}

func (d *tmuxDriver) SessionAlive() bool {
	_, err := d.sh([]string{"tmux", "has-session", "-t", d.session}, nil)
	return err == nil
}
