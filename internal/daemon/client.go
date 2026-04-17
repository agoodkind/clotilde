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

// NudgeDiscoveryScan asks the daemon to run an immediate discovery
// scan. The preferred path is the TriggerScan RPC because it carries
// no privileges and works regardless of how the daemon was launched.
// SIGUSR1 is the fallback for callers that already have a PID and
// cannot or do not want to open a gRPC connection.
func NudgeDiscoveryScan() {
	if c, err := ConnectOrStart(context.Background()); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, _ = c.rpc.TriggerScan(ctx, &daemonpb.TriggerScanRequest{})
		c.conn.Close()
		return
	}
	pid, err := findDaemonPID()
	if err != nil || pid <= 0 {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGUSR1)
}

// RenameSessionViaDaemon asks the daemon to perform the rename. The
// daemon owns the rename so subscribers get notified, and concurrent
// callers do not race on the metadata.json move. Returns true when
// the rename happened via the daemon, false (and an error) when the
// daemon is unreachable so callers can fall back to a direct write.
func RenameSessionViaDaemon(ctx context.Context, oldName, newName string) (bool, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.RenameSession(rpcCtx, &daemonpb.RenameSessionRequest{
		OldName: oldName,
		NewName: newName,
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// DeleteSessionViaDaemon asks the daemon to drop a session from the
// registry. Transcript and agent log cleanup remain the caller's job
// because they touch per-project state outside the daemon's view.
func DeleteSessionViaDaemon(ctx context.Context, name string) (bool, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.DeleteSession(rpcCtx, &daemonpb.DeleteSessionRequest{Name: name})
	if err != nil {
		return false, err
	}
	return true, nil
}

// SubscribeRegistry opens a long-lived stream of RegistryEvent values
// from the daemon. The returned channel closes when the stream
// terminates for any reason. Callers can stop subscribing by calling
// the returned cancel function. The channel buffers a few events so a
// slow consumer does not lose recent activity, but the daemon will
// drop events once a per-subscriber buffer fills.
func SubscribeRegistry(parent context.Context) (<-chan *daemonpb.RegistryEvent, context.CancelFunc, error) {
	c, err := ConnectOrStart(parent)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	stream, err := c.rpc.SubscribeRegistry(ctx, &daemonpb.SubscribeRegistryRequest{})
	if err != nil {
		cancel()
		c.conn.Close()
		return nil, nil, err
	}
	out := make(chan *daemonpb.RegistryEvent, 8)
	go func() {
		defer close(out)
		defer c.conn.Close()
		for {
			ev, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel, nil
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

// UpdateSessionRemoteControlViaDaemon flips the per session remote
// control flag through the daemon. Returns true when the daemon
// accepted the update so callers can fall back to a direct file write
// only when the daemon is unreachable.
func UpdateSessionRemoteControlViaDaemon(ctx context.Context, name string, enabled bool) (bool, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.UpdateSessionSettings(rpcCtx, &daemonpb.UpdateSessionSettingsRequest{
		Name:       name,
		Settings:   &daemonpb.Settings{RemoteControl: enabled},
		UpdateMask: []string{"remote_control"},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// UpdateGlobalRemoteControlViaDaemon flips the global default. The
// daemon serialises writes to ~/.config/clotilde/config.toml.
func UpdateGlobalRemoteControlViaDaemon(ctx context.Context, enabled bool) (bool, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.UpdateGlobalSettings(rpcCtx, &daemonpb.UpdateGlobalSettingsRequest{
		Defaults:   &daemonpb.GlobalDefaults{RemoteControl: enabled},
		UpdateMask: []string{"remote_control"},
	})
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListBridgesViaDaemon fetches the daemon's current bridge map.
func ListBridgesViaDaemon(ctx context.Context) ([]*daemonpb.Bridge, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return nil, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := c.rpc.ListBridges(rpcCtx, &daemonpb.ListBridgesRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Bridges, nil
}

// SendToSessionViaDaemon delivers text into a running claude session
// through its inject socket. The daemon resolves the session id to a
// socket path and forwards the bytes. Returns false when no listener
// is present (session not running, or wrapper does not own a pty).
func SendToSessionViaDaemon(ctx context.Context, sessionID, text string) (bool, error) {
	c, err := ConnectOrStart(ctx)
	if err != nil {
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.rpc.SendToSession(rpcCtx, &daemonpb.SendToSessionRequest{
		SessionId: sessionID,
		Text:      text,
	})
	if err != nil {
		return false, err
	}
	return resp.GetDelivered(), nil
}

// TailTranscriptViaDaemon opens the daemon's transcript stream for
// the given session id. The returned channel closes when the stream
// terminates. Calling cancel stops the subscription.
func TailTranscriptViaDaemon(parent context.Context, sessionID string, startOffset int64) (<-chan *daemonpb.TranscriptLine, context.CancelFunc, error) {
	c, err := ConnectOrStart(parent)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	stream, err := c.rpc.TailTranscript(ctx, &daemonpb.TailTranscriptRequest{
		SessionId:     sessionID,
		StartAtOffset: startOffset,
	})
	if err != nil {
		cancel()
		c.conn.Close()
		return nil, nil, err
	}
	out := make(chan *daemonpb.TranscriptLine, 64)
	go func() {
		defer close(out)
		defer c.conn.Close()
		for {
			line, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case out <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, cancel, nil
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
