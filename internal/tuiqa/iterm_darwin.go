//go:build darwin

package tuiqa

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type itermDriver struct {
	sessionTag string
	sessionID  string
	started    bool
}

func newITermDriver() (Driver, error) {
	pid := os.Getpid()
	return &itermDriver{
		sessionTag: fmt.Sprintf("clyde-tuiqa-%d", pid),
	}, nil
}

// AttachITermSession reconnects AppleScript operations to an existing iTerm session id
// (for multi-invocation workflows after session-start printed the id).
func AttachITermSession(sessionID string) Driver {
	return &itermDriver{
		sessionID: sessionID,
		started:   sessionID != "",
	}
}

// ITermSessionID returns the AppleScript session id after Start, or empty.
func ITermSessionID(d Driver) string {
	iw, ok := d.(*itermDriver)
	if !ok {
		return ""
	}
	return iw.sessionID
}

func (d *itermDriver) Name() string { return "iterm" }

func (d *itermDriver) osa(script string) string {
	cmd := exec.Command("/usr/bin/osascript", "-e", script)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out))
}

func (d *itermDriver) escapeForWriteText(b []byte) string {
	var parts []string
	var lit strings.Builder
	flush := func() {
		if lit.Len() == 0 {
			return
		}
		s := lit.String()
		s = strings.ReplaceAll(s, "\\", "\\\\")
		s = strings.ReplaceAll(s, "\"", "\\\"")
		parts = append(parts, "\""+s+"\"")
		lit.Reset()
	}
	for _, bb := range b {
		switch bb {
		case 0x1B:
			flush()
			parts = append(parts, "(ASCII character 27)")
		case 0x0D:
			flush()
			parts = append(parts, "(ASCII character 13)")
		case 0x0A:
			flush()
			parts = append(parts, "(ASCII character 10)")
		case 0x09:
			flush()
			parts = append(parts, "(ASCII character 9)")
		case 0x7F:
			flush()
			parts = append(parts, "(ASCII character 127)")
		case 0x08:
			flush()
			parts = append(parts, "(ASCII character 8)")
		default:
			if bb >= 0x20 && bb < 0x7F {
				lit.WriteByte(bb)
			} else {
				flush()
				parts = append(parts, "(ASCII character "+strconv.Itoa(int(bb))+")")
			}
		}
	}
	flush()
	if len(parts) == 0 {
		return "\"\""
	}
	return strings.Join(parts, " & ")
}

func (d *itermDriver) cleanupAllTuiqaWindows() {
	const script = `
tell application "iTerm"
  try
    set toClose to {}
    repeat with w in windows
      try
        set sess to current session of w
        set shouldClose to false
        try
          if name of sess starts with "clyde-tuiqa-" then set shouldClose to true
        end try
        try
          if is processing of sess is false then set shouldClose to true
        end try
        if shouldClose then copy w to end of toClose
      end try
    end repeat
    repeat with w in toClose
      try
        close w
      end try
    end repeat
  end try
end tell
`
	_ = d.osa(script)
}

func (d *itermDriver) Start(binaryPath string, env []string, cols, rows int) error {
	_ = cols
	_ = rows
	d.cleanupAllTuiqaWindows()
	abs := binaryPath
	if !filepath.IsAbs(abs) {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("iterm: abs path: %w", err)
		}
		abs = filepath.Join(wd, abs)
	}
	var cmdLine strings.Builder
	cmdLine.WriteString("/usr/bin/env")
	for _, e := range env {
		cmdLine.WriteString(" ")
		cmdLine.WriteString(e)
	}
	cmdLine.WriteString(" ")
	cmdLine.WriteString(abs)
	escaped := strings.ReplaceAll(cmdLine.String(), "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	script := fmt.Sprintf(`
tell application "iTerm"
  activate
  set newWindow to (create window with default profile command "%s")
  set theSession to current session of newWindow
  tell theSession
    set name to "%s"
  end tell
  return id of theSession
end tell
`, escaped, d.sessionTag)
	d.sessionID = d.osa(script)
	d.started = d.sessionID != ""
	time.Sleep(800 * time.Millisecond)
	if !d.started {
		return fmt.Errorf("iterm: empty session id from AppleScript")
	}
	return nil
}

func (d *itermDriver) Kill() error {
	if !d.started {
		return nil
	}
	if d.sessionID != "" {
		esc := strings.ReplaceAll(d.sessionID, "\\", "\\\\")
		script := fmt.Sprintf(`
tell application "iTerm"
  try
    repeat with w in windows
      repeat with t in tabs of w
        repeat with s in sessions of t
          try
            if (id of s as string) is "%s" then
              close w
              exit repeat
            end if
          end try
        end repeat
      end repeat
    end try
  end tell
`, esc)
		_ = d.osa(script)
	}
	d.cleanupAllTuiqaWindows()
	d.started = false
	return nil
}

func (d *itermDriver) scriptTargetingSession(body string) string {
	esc := strings.ReplaceAll(d.sessionID, "\\", "\\\\")
	return fmt.Sprintf(`
tell application "iTerm"
  try
    repeat with w in windows
      repeat with t in tabs of w
        repeat with s in sessions of t
          try
            if (id of s as string) is "%s" then
              %s
            end if
          end try
        end repeat
      end repeat
    end repeat
  end try
  return ""
end tell
`, esc, body)
}

func (d *itermDriver) Capture() (string, error) {
	if d.sessionID == "" {
		return "", nil
	}
	return d.osa(d.scriptTargetingSession("return contents of s")), nil
}

func (d *itermDriver) SendKey(tokens []string) error {
	var b []byte
	for _, tok := range tokens {
		b = append(b, TokenToBytes(tok)...)
	}
	return d.PasteRaw(b)
}

func (d *itermDriver) PasteRaw(data []byte) error {
	if d.sessionID == "" {
		return fmt.Errorf("iterm: not started")
	}
	payload := d.escapeForWriteText(data)
	body := fmt.Sprintf(`
tell s
  write text %s newline NO
end tell
`, payload)
	_ = d.osa(d.scriptTargetingSession(body))
	return nil
}

func (d *itermDriver) SessionAlive() bool {
	if d.sessionID == "" {
		return false
	}
	r := d.osa(d.scriptTargetingSession(`
if is processing of s then
  return "yes"
end if
`))
	return r == "yes"
}
