// PTY wrapper for sessions launched with remote control.
//
// When a session opts into remote control, clyde owns claude's
// stdio so external clients (the dashboard sidecar, the daemon's
// SendToSession RPC) can inject text into claude as if the user
// typed it. The wrapper opens a per session Unix socket and copies
// any bytes received there into claude's pty stdin. Local terminal
// input still flows through unchanged. Output flows back to the
// terminal so the user keeps their normal in terminal experience.
package claude

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"

	"goodkind.io/clyde/internal/daemon"
)

// invokeInteractivePTY runs claude inside a pty. The terminal is put
// into raw mode so claude's TUI rendering survives. A per session
// Unix socket accepts text from external clients (daemon
// SendToSession path) and writes the bytes into the pty's stdin.
// Returns when claude exits.
func invokeInteractivePTY(args []string, env map[string]string, workDir, sessionID string) error {
	return invokePTY(args, env, workDir, sessionID, true)
}

// StartHeadlessRemoteWorker runs Claude with remote control enabled in a PTY
// that is owned by a background process instead of an interactive terminal.
// The wrapper still exposes the inject socket so the daemon sidecar can send
// text to the running Claude session, but local terminal IO is detached.
func StartHeadlessRemoteWorker(env map[string]string, settingsFile string, workDir, sessionID string) error {
	args := []string{}
	args = appendCommonArgs(args, settingsFile)
	if sessionID != "" {
		args = append(args, "--session-id", sessionID)
	}
	return invokePTY(args, env, workDir, sessionID, false)
}

func invokePTY(args []string, env map[string]string, workDir, sessionID string, interactive bool) error {
	claudeBin := ClaudeBinaryPathFunc()

	// Daemon acquire mirrors the classic invokeInteractive path so
	// per session settings and the wrapper id flow consistently.
	ctx := context.Background()
	wrapperID := fmt.Sprintf("%d", os.Getpid())
	sessionName := env["CLYDE_SESSION_NAME"]
	client, err := daemon.ConnectOrStart(ctx)
	if err == nil {
		if resp, acqErr := client.AcquireSession(wrapperID, sessionName); acqErr == nil {
			args = append([]string{"--settings", resp.SettingsFile}, args...)
		}
		client.Close()
	}

	if interactive {
		displayCommand(claudeBin, args, env)
	} else {
		slog.Info("wrapper.remote_headless.starting",
			"component", "wrapper",
			"session", sessionName,
			"session_id", sessionID,
			"workdir", workDir,
		)
	}

	cmd := exec.Command(claudeBin, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	defer ptmx.Close()

	if interactive {
		// Forward initial size and propagate window changes so claude's
		// TUI matches the terminal dimensions.
		winchCh := make(chan os.Signal, 1)
		signal.Notify(winchCh, syscall.SIGWINCH)
		defer signal.Stop(winchCh)
		go func() {
			for range winchCh {
				_ = pty.InheritSize(os.Stdin, ptmx)
			}
		}()
		winchCh <- syscall.SIGWINCH

		// Switch the terminal into raw mode for the duration of the
		// session so claude's rendering is not garbled by line discipline.
		oldState, _ := term.MakeRaw(int(os.Stdin.Fd()))
		defer func() {
			if oldState != nil {
				_ = term.Restore(int(os.Stdin.Fd()), oldState)
			}
		}()
	} else {
		_ = pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120})
	}

	// Inject socket lifecycle. The daemon dials this socket from
	// SendToSession to forward text to the running claude process.
	socketPath := injectSocketPathFor(sessionID)
	listener, lerr := openInjectListener(socketPath)
	if lerr == nil {
		defer func() {
			_ = listener.Close()
			_ = os.Remove(socketPath)
		}()
		go acceptInjectConns(listener, ptmx)
	}

	// Copy goroutines for pty <-> terminal.
	done := make(chan struct{})
	var copyOnce sync.Once
	finish := func() { copyOnce.Do(func() { close(done) }) }

	if interactive {
		go func() {
			_, _ = io.Copy(ptmx, os.Stdin)
			finish()
		}()
		go func() {
			_, _ = io.Copy(os.Stdout, ptmx)
			finish()
		}()
	} else {
		go func() {
			_, _ = io.Copy(io.Discard, ptmx)
			finish()
		}()
	}

	// Daemon settings sync runs alongside the pty path too so global
	// settings changes propagate to this session like any other.
	monitorDone := make(chan struct{})
	monitorStopped := make(chan struct{})
	monitor := &monitorState{}
	go monitorDaemon(ctx, wrapperID, sessionName, monitorDone, monitor, monitorStopped)

	runErr := cmd.Wait()
	close(monitorDone)
	<-monitorStopped
	finish()
	<-done
	if interactive && shouldSelfReloadWrapper(env, runErr, monitor) {
		if reloadErr := selfReloadCurrentProcess(); reloadErr != nil {
			return fmt.Errorf("self reload: %w", reloadErr)
		}
	}
	return runErr
}

// injectSocketPathFor returns the path of the inject socket the
// daemon will dial for sessionID. The directory is created lazily
// here so the daemon can stat the file without racing the wrapper.
func injectSocketPathFor(sessionID string) string {
	dir := injectSocketDir()
	_ = os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, sessionID+".sock")
}

// injectSocketDir matches the daemon's resolution. Kept duplicated to
// avoid an import cycle between claude and daemon.
func injectSocketDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "clyde", "inject")
	}
	return filepath.Join(os.TempDir(), "clyde-inject")
}

// openInjectListener removes any stale socket from a previous run
// and opens the listener. Failures return nil so the pty wrapper can
// continue without injection support.
func openInjectListener(path string) (net.Listener, error) {
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = l.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return l, nil
}

// acceptInjectConns reads any bytes a connecting client sends and
// writes them into the pty's stdin. Each connection is short lived:
// the daemon writes, then closes.
func acceptInjectConns(l net.Listener, ptmx io.Writer) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, _ = io.Copy(ptmx, c)
		}(conn)
	}
}
