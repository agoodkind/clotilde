// Package ui implements the Clyde terminal user interface.
// "Copy bridge URL" action. The helper picks the first clipboard tool
// that matches the host OS. Callers get a single error return so they
// can surface a message when the clipboard is unreachable.
package ui

import (
	"errors"
	"io"
	"os/exec"
	"runtime"
)

// CopyToClipboard writes text to the system clipboard using the first
// compatible tool found on PATH. Returns an error when no supported
// tool is available or the tool fails to run.
//
// macOS: pbcopy (ships with the OS).
// Linux under Wayland: wl-copy.
// Linux under X11: xclip, then xsel as a fallback.
// Windows: clip.exe.
//
// Unknown OS returns an error without touching the clipboard so the
// caller can log a clear message.
func CopyToClipboard(text string) error {
	for _, candidate := range clipboardCandidates() {
		if _, err := exec.LookPath(candidate.Bin); err != nil {
			continue
		}
		cmd := exec.Command(candidate.Bin, candidate.Args...)
		stdin, err := cmd.StdinPipe()
		if err != nil {
			continue
		}
		if err := cmd.Start(); err != nil {
			continue
		}
		go func() {
			_, _ = io.WriteString(stdin, text)
			_ = stdin.Close()
		}()
		if err := cmd.Wait(); err != nil {
			return err
		}
		return nil
	}
	return errors.New("no clipboard tool available (install pbcopy, wl-copy, xclip, xsel, or clip.exe)")
}

// clipboardCandidate is one tool that can populate the clipboard.
// Bin is the executable to look up. Args are passed verbatim.
type clipboardCandidate struct {
	Bin  string
	Args []string
}

// clipboardCandidates returns the ordered set of tools to try for the
// current host. Each entry is tried in order until one exists on PATH.
func clipboardCandidates() []clipboardCandidate {
	switch runtime.GOOS {
	case "darwin":
		return []clipboardCandidate{{Bin: "pbcopy"}}
	case "windows":
		return []clipboardCandidate{{Bin: "clip.exe"}, {Bin: "clip"}}
	case "linux":
		return []clipboardCandidate{
			{Bin: "wl-copy"},
			{Bin: "xclip", Args: []string{"-selection", "clipboard"}},
			{Bin: "xsel", Args: []string{"--clipboard", "--input"}},
		}
	case "freebsd", "openbsd", "netbsd":
		return []clipboardCandidate{
			{Bin: "xclip", Args: []string{"-selection", "clipboard"}},
			{Bin: "xsel", Args: []string{"--clipboard", "--input"}},
		}
	}
	return nil
}
