package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/fgrehm/clotilde/api/daemonpb"
	"github.com/fgrehm/clotilde/internal/config"
)

// Client is a gRPC client for the clotilde daemon.
type Client struct {
	conn *grpc.ClientConn
	rpc  daemonpb.AgentGateDClient
}

// NudgeDiscoveryScan sends SIGUSR1 to the running daemon so its scanner
// wakes up and runs an immediate scan instead of waiting for the next
// 5 minute tick. The function looks up the daemon by reading the launchd
// pid file or by walking pgrep-style; failures are silent because the
// nudge is purely an optimization.
func NudgeDiscoveryScan() {
	pid, err := findDaemonPID()
	if err != nil || pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGUSR1)
}

// findDaemonPID locates the running daemon's pid via launchctl. Returns
// 0 with no error when the daemon is not registered.
func findDaemonPID() (int, error) {
	out, err := exec.Command("launchctl", "list", "io.goodkind.clotilde.daemon").Output()
	if err != nil {
		return 0, err
	}
	for _, line := range splitLines(string(out)) {
		line = trimSpace(line)
		if !startsWith(line, `"PID" = `) {
			continue
		}
		raw := line[len(`"PID" = `):]
		raw = trimRight(raw, ";")
		raw = trimSpace(raw)
		n, convErr := strconv.Atoi(raw)
		if convErr != nil {
			return 0, convErr
		}
		return n, nil
	}
	return 0, nil
}

// Tiny string helpers kept local so the daemon client does not depend
// on strings just for this one nudge function.
func splitLines(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func trimRight(s, suffix string) string {
	for len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// connect opens a connection to a running daemon and verifies it responds.
func connect(ctx context.Context) (*Client, error) {
	socketPath := config.DaemonSocketPath()
	target := "unix://" + socketPath

	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return nil, fmt.Errorf("dial daemon at %s: %w", socketPath, err)
	}

	// Verify the daemon is alive by making a trivial RPC.
	client := &Client{conn: conn, rpc: daemonpb.NewAgentGateDClient(conn)}
	_, err = client.rpc.AcquireSession(dialCtx, &daemonpb.AcquireSessionRequest{
		WrapperId: "__probe__",
	})
	// InvalidArgument means the daemon is alive (it rejected the probe).
	// Any other error means it's dead or unreachable.
	if err != nil {
		// Check if the error is "connection refused" or similar transport error
		// vs a legitimate RPC error (which means daemon IS running).
		// gRPC returns codes.Unavailable for transport failures.
		if isTransportError(err) {
			conn.Close()
			return nil, fmt.Errorf("daemon not responding: %w", err)
		}
	}

	return client, nil
}

// isTransportError returns true if the gRPC error indicates the daemon
// is not reachable (vs a legitimate application-level error).
func isTransportError(err error) bool {
	s := err.Error()
	// gRPC wraps transport failures with "Unavailable" or connection errors.
	for _, substr := range []string{"Unavailable", "connection refused", "no such file"} {
		if contains(s, substr) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ConnectOrStart connects to the daemon, starting it if not running.
// Uses flock to prevent multiple processes from spawning the daemon
// simultaneously.
func ConnectOrStart(ctx context.Context) (*Client, error) {
	// Fast path: daemon is already running.
	if client, err := connect(ctx); err == nil {
		return client, nil
	}

	// Slow path: need to start daemon. Take flock to serialize.
	lockPath := filepath.Join(config.RuntimeDir(), "daemon.lock")
	if err := config.EnsureRuntimeDir(); err != nil {
		return nil, err
	}

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	defer lockFile.Close()

	// Block until we get the lock.
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return nil, fmt.Errorf("flock: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	// Double-check: another process may have started the daemon while we waited.
	if client, err := connect(ctx); err == nil {
		return client, nil
	}

	// We hold the lock and daemon is not running. Start it.
	// Prefer launchctl on macOS (if the agent is registered), fall back to direct spawn.
	if err := startDaemon(); err != nil {
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	// Retry with backoff until daemon is ready.
	delay := 50 * time.Millisecond
	for range 8 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if client, err := connect(ctx); err == nil {
			return client, nil
		}
		delay = min(delay*2, 500*time.Millisecond)
	}

	return nil, fmt.Errorf("daemon did not become ready after start")
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// AcquireSession asks the daemon to create a per-session settings file.
func (c *Client) AcquireSession(wrapperID, sessionName string) (*daemonpb.AcquireSessionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return c.rpc.AcquireSession(ctx, &daemonpb.AcquireSessionRequest{
		WrapperId:   wrapperID,
		SessionName: sessionName,
	})
}

// ReleaseSession notifies the daemon that this wrapper process has exited.
func (c *Client) ReleaseSession(wrapperID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.rpc.ReleaseSession(ctx, &daemonpb.ReleaseSessionRequest{
		WrapperId: wrapperID,
	})
	return err
}

// UpdateContext asks the daemon to generate a context summary for a session
// in the background. Fire-and-forget: the caller does not wait for the result.
// Messages should be pre-extracted by the caller (to avoid import cycles).
func (c *Client) UpdateContext(sessionName, workspaceRoot string, messages []string) error {
	payload, _ := json.Marshal(map[string]interface{}{
		"type":           "update_context",
		"session_name":   sessionName,
		"workspace_root": workspaceRoot,
		"messages":       messages,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.rpc.HookEvent(ctx, &daemonpb.HookEventRequest{
		RawJson: payload,
	})
	return err
}

const launchAgentLabel = "io.goodkind.clotilde.daemon"

// startDaemon starts the daemon process. On macOS, tries launchctl kickstart
// first (if the LaunchAgent is registered), falling back to direct spawn.
func startDaemon() error {
	if runtime.GOOS == "darwin" {
		uid := strconv.Itoa(os.Getuid())
		target := "gui/" + uid + "/" + launchAgentLabel
		if err := exec.Command("launchctl", "kickstart", target).Run(); err == nil {
			return nil // launchd started it
		}
		// launchctl failed (agent not registered) — fall through to direct spawn
	}

	return spawnDaemonDirect()
}

// spawnDaemonDirect starts the daemon as a detached child process.
func spawnDaemonDirect() error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}

	daemonCmd := exec.Command(self, "daemon")
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := daemonCmd.Start(); err != nil {
		return err
	}
	go func() { _ = daemonCmd.Wait() }()
	return nil
}

// ListActiveSessions returns the names of all currently active sessions.
func (c *Client) ListActiveSessions() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := c.rpc.ListActiveSessions(ctx, &daemonpb.ListActiveSessionsRequest{})
	if err != nil {
		return nil, err
	}

	var names []string
	for _, s := range resp.Sessions {
		if s.SessionName != "" {
			names = append(names, s.SessionName)
		}
	}
	return names, nil
}
