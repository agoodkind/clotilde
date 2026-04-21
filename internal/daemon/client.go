package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/gklog"
)

func daemonClientLog(ctx context.Context) *slog.Logger {
	if ctx == nil {
		ctx = context.Background()
	}
	return gklog.LoggerFromContext(ctx).With("component", "daemon", "subcomponent", "client")
}

// Client is a gRPC client for the clyde daemon.
type Client struct {
	conn *grpc.ClientConn
	rpc  clydev1.ClydeServiceClient
}

// NudgeDiscoveryScan asks the daemon to run an immediate discovery
// scan. The preferred path is the TriggerScan RPC because it carries
// no privileges and works regardless of how the daemon was launched.
// SIGUSR1 is the fallback for callers that already have a PID and
// cannot or do not want to open a gRPC connection.
func NudgeDiscoveryScan() {
	bg := context.Background()
	log := daemonClientLog(bg)
	log.DebugContext(bg, "daemon.client.nudge.begin")
	c, err := ConnectOrStart(bg)
	if err == nil {
		rpcCtx, cancel := context.WithTimeout(bg, 2*time.Second)
		defer cancel()
		log.DebugContext(rpcCtx, "daemon.client.nudge.trigger_scan_rpc")
		_, _ = c.rpc.TriggerScan(rpcCtx, &clydev1.TriggerScanRequest{})
		c.conn.Close()
		return
	}
	log.DebugContext(bg, "daemon.client.nudge.connect_failed_try_sigusr1", "err", err)
	pid, pidErr := findDaemonPID()
	if pidErr != nil || pid <= 0 {
		log.DebugContext(bg, "daemon.client.nudge.pid_lookup_skipped",
			"pid", pid,
			"err", pidErr,
		)
		return
	}
	if sigErr := syscall.Kill(pid, syscall.SIGUSR1); sigErr != nil {
		log.DebugContext(bg, "daemon.client.nudge.sigusr1_failed", "pid", pid, "err", sigErr)
		return
	}
	log.DebugContext(bg, "daemon.client.nudge.sigusr1_sent", "pid", pid)
}

// RenameSessionViaDaemon asks the daemon to perform the rename. The
// daemon owns the rename so subscribers get notified, and concurrent
// callers do not race on the metadata.json move. Returns true when
// the rename happened via the daemon, false (and an error) when the
// daemon is unreachable so callers can fall back to a direct write.
func RenameSessionViaDaemon(ctx context.Context, oldName, newName string) (bool, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.rename_session.begin",
		"old_name", oldName,
		"new_name", newName,
	)
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.rename_session.connect_failed", "err", err)
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.RenameSession(rpcCtx, &clydev1.RenameSessionRequest{
		OldName: oldName,
		NewName: newName,
	})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.rename_session.rpc_failed", "err", err)
		return false, err
	}
	log.DebugContext(rpcCtx, "daemon.client.rename_session.ok")
	return true, nil
}

// DeleteSessionViaDaemon asks the daemon to drop a session from the
// registry. Transcript and agent log cleanup remain the caller's job
// because they touch per-project state outside the daemon's view.
func DeleteSessionViaDaemon(ctx context.Context, name string) (bool, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.delete_session.begin", "name", name)
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.delete_session.connect_failed", "err", err)
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.DeleteSession(rpcCtx, &clydev1.DeleteSessionRequest{Name: name})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.delete_session.rpc_failed", "err", err)
		return false, err
	}
	log.DebugContext(rpcCtx, "daemon.client.delete_session.ok")
	return true, nil
}

// SubscribeRegistry opens a long-lived stream of SubscribeRegistryResponse values
// from the daemon. The returned channel closes when the stream
// terminates for any reason. Callers can stop subscribing by calling
// the returned cancel function. The channel buffers a few events so a
// slow consumer does not lose recent activity, but the daemon will
// drop events once a per-subscriber buffer fills.
func SubscribeRegistry(parent context.Context) (<-chan *clydev1.SubscribeRegistryResponse, context.CancelFunc, error) {
	log := daemonClientLog(parent)
	log.DebugContext(parent, "daemon.client.subscribe_registry.begin")
	c, err := ConnectOrStart(parent)
	if err != nil {
		log.DebugContext(parent, "daemon.client.subscribe_registry.connect_failed", "err", err)
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	stream, err := c.rpc.SubscribeRegistry(ctx, &clydev1.SubscribeRegistryRequest{})
	if err != nil {
		log.DebugContext(ctx, "daemon.client.subscribe_registry.stream_open_failed", "err", err)
		cancel()
		c.conn.Close()
		return nil, nil, err
	}
	log.DebugContext(ctx, "daemon.client.subscribe_registry.stream_open")
	out := make(chan *clydev1.SubscribeRegistryResponse, 8)
	go func() {
		defer close(out)
		defer c.conn.Close()
		loopLog := daemonClientLog(ctx)
		for {
			ev, err := stream.Recv()
			if err != nil {
				loopLog.DebugContext(ctx, "daemon.client.subscribe_registry.recv_done", "err", err)
				return
			}
			select {
			case out <- ev:
			case <-ctx.Done():
				loopLog.DebugContext(ctx, "daemon.client.subscribe_registry.consumer_cancelled")
				return
			}
		}
	}()
	return out, cancel, nil
}

// findDaemonPID locates the running daemon's pid via launchctl. Returns
// 0 with no error when the daemon is not registered.
func findDaemonPID() (int, error) {
	bg := context.Background()
	log := daemonClientLog(bg)
	out, err := exec.Command("launchctl", "list", "io.goodkind.clyde.daemon").Output()
	if err != nil {
		log.DebugContext(bg, "daemon.client.find_pid.launchctl_failed", "err", err)
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
			log.DebugContext(bg, "daemon.client.find_pid.pid_parse_failed", "err", convErr)
			return 0, convErr
		}
		log.DebugContext(bg, "daemon.client.find_pid.ok", "pid", n)
		return n, nil
	}
	log.DebugContext(bg, "daemon.client.find_pid.no_pid_field")
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
	log := daemonClientLog(ctx)
	socketPath := config.DaemonSocketPath()
	target := "unix://" + socketPath
	log.DebugContext(ctx, "daemon.client.connect.dial", "socket_path", socketPath)

	dialCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.connect.new_client_failed", "err", err)
		return nil, fmt.Errorf("dial daemon at %s: %w", socketPath, err)
	}

	// Verify the daemon is alive by making a trivial RPC.
	client := &Client{conn: conn, rpc: clydev1.NewClydeServiceClient(conn)}
	_, err = client.rpc.AcquireSession(dialCtx, &clydev1.AcquireSessionRequest{
		WrapperId: "__probe__",
	})
	// InvalidArgument means the daemon is alive (it rejected the probe).
	// Any other error means it's dead or unreachable.
	if err != nil {
		// Check if the error is "connection refused" or similar transport error
		// vs a legitimate RPC error (which means daemon IS running).
		// gRPC returns codes.Unavailable for transport failures.
		if isTransportError(err) {
			log.DebugContext(dialCtx, "daemon.client.connect.probe_transport_error", "err", err)
			conn.Close()
			return nil, fmt.Errorf("daemon not responding: %w", err)
		}
		log.DebugContext(dialCtx, "daemon.client.connect.probe_alive", "err", err)
	}

	log.DebugContext(ctx, "daemon.client.connect.ok")
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
	log := daemonClientLog(ctx)
	// Tests and CI can disable the daemon entirely by setting
	// CLYDE_DISABLE_DAEMON=1. The wrapper then behaves as if the
	// daemon is unreachable, which gives the classic stdio path
	// without settings injection, global sync, or context summary
	// side effects.
	if os.Getenv("CLYDE_DISABLE_DAEMON") != "" {
		log.DebugContext(ctx, "daemon.client.connect_or_start.disabled")
		return nil, fmt.Errorf("daemon disabled by CLYDE_DISABLE_DAEMON")
	}
	// Fast path: daemon is already running.
	if client, err := connect(ctx); err == nil {
		log.DebugContext(ctx, "daemon.client.connect_or_start.fast_path")
		return client, nil
	}

	log.DebugContext(ctx, "daemon.client.connect_or_start.slow_path")
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
		log.DebugContext(ctx, "daemon.client.connect_or_start.ready_after_lock_wait")
		return client, nil
	}

	log.DebugContext(ctx, "daemon.client.connect_or_start.starting_daemon")
	// We hold the lock and daemon is not running. Start it.
	// Prefer launchctl on macOS (if the agent is registered), fall back to direct spawn.
	if err := startDaemon(); err != nil {
		log.DebugContext(ctx, "daemon.client.connect_or_start.start_daemon_failed", "err", err)
		return nil, fmt.Errorf("start daemon: %w", err)
	}

	// Retry with backoff until daemon is ready.
	delay := 50 * time.Millisecond
	for attempt := range 8 {
		select {
		case <-ctx.Done():
			log.DebugContext(ctx, "daemon.client.connect_or_start.wait_cancelled")
			return nil, ctx.Err()
		case <-time.After(delay):
		}
		if client, err := connect(ctx); err == nil {
			log.DebugContext(ctx, "daemon.client.connect_or_start.ready_after_start", "attempt", attempt)
			return client, nil
		}
		delay = min(delay*2, 500*time.Millisecond)
	}

	log.DebugContext(ctx, "daemon.client.connect_or_start.not_ready_after_retries")
	return nil, fmt.Errorf("daemon did not become ready after start")
}

// Close closes the gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}

// AcquireSession asks the daemon to create a per-session settings file.
func (c *Client) AcquireSession(wrapperID, sessionName string) (*clydev1.AcquireSessionResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.acquire_session.begin",
		"wrapper_id", wrapperID,
		"session_name", sessionName,
	)
	return c.rpc.AcquireSession(ctx, &clydev1.AcquireSessionRequest{
		WrapperId:   wrapperID,
		SessionName: sessionName,
	})
}

// ReleaseSession notifies the daemon that this wrapper process has exited.
func (c *Client) ReleaseSession(wrapperID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.release_session.begin", "wrapper_id", wrapperID)
	_, err := c.rpc.ReleaseSession(ctx, &clydev1.ReleaseSessionRequest{
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
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.update_context.begin",
		"session_name", sessionName,
		"workspace_root", workspaceRoot,
		"message_count", len(messages),
	)
	_, err := c.rpc.HookEvent(ctx, &clydev1.HookEventRequest{
		RawJson: payload,
	})
	return err
}

// UpdateSessionRemoteControlViaDaemon flips the per session remote
// control flag through the daemon. Returns true when the daemon
// accepted the update so callers can fall back to a direct file write
// only when the daemon is unreachable.
func UpdateSessionRemoteControlViaDaemon(ctx context.Context, name string, enabled bool) (bool, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.update_session_remote_control.begin",
		"name", name,
		"enabled", enabled,
	)
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.update_session_remote_control.connect_failed", "err", err)
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.UpdateSessionSettings(rpcCtx, &clydev1.UpdateSessionSettingsRequest{
		Name:       name,
		Settings:   &clydev1.Settings{RemoteControl: enabled},
		UpdateMask: []string{"remote_control"},
	})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.update_session_remote_control.rpc_failed", "err", err)
		return false, err
	}
	log.DebugContext(rpcCtx, "daemon.client.update_session_remote_control.ok")
	return true, nil
}

// UpdateGlobalRemoteControlViaDaemon flips the global default. The
// daemon serialises writes to ~/.config/clyde/config.toml.
func UpdateGlobalRemoteControlViaDaemon(ctx context.Context, enabled bool) (bool, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.update_global_remote_control.begin", "enabled", enabled)
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.update_global_remote_control.connect_failed", "err", err)
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = c.rpc.UpdateGlobalSettings(rpcCtx, &clydev1.UpdateGlobalSettingsRequest{
		Defaults:   &clydev1.GlobalDefaults{RemoteControl: enabled},
		UpdateMask: []string{"remote_control"},
	})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.update_global_remote_control.rpc_failed", "err", err)
		return false, err
	}
	log.DebugContext(rpcCtx, "daemon.client.update_global_remote_control.ok")
	return true, nil
}

// ListBridgesViaDaemon fetches the daemon's current bridge map.
func ListBridgesViaDaemon(ctx context.Context) ([]*clydev1.Bridge, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.list_bridges.begin")
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.list_bridges.connect_failed", "err", err)
		return nil, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resp, err := c.rpc.ListBridges(rpcCtx, &clydev1.ListBridgesRequest{})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.list_bridges.rpc_failed", "err", err)
		return nil, err
	}
	log.DebugContext(rpcCtx, "daemon.client.list_bridges.ok", "count", len(resp.Bridges))
	return resp.Bridges, nil
}

// SendToSessionViaDaemon delivers text into a running claude session
// through its inject socket. The daemon resolves the session id to a
// socket path and forwards the bytes. Returns false when no listener
// is present (session not running, or wrapper does not own a pty).
func SendToSessionViaDaemon(ctx context.Context, sessionID, text string) (bool, error) {
	log := daemonClientLog(ctx)
	log.DebugContext(ctx, "daemon.client.send_to_session.begin",
		"session_id", sessionID,
		"text_len", len(text),
	)
	c, err := ConnectOrStart(ctx)
	if err != nil {
		log.DebugContext(ctx, "daemon.client.send_to_session.connect_failed", "err", err)
		return false, err
	}
	defer c.conn.Close()
	rpcCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	resp, err := c.rpc.SendToSession(rpcCtx, &clydev1.SendToSessionRequest{
		SessionId: sessionID,
		Text:      text,
	})
	if err != nil {
		log.DebugContext(rpcCtx, "daemon.client.send_to_session.rpc_failed", "err", err)
		return false, err
	}
	delivered := resp.GetDelivered()
	log.DebugContext(rpcCtx, "daemon.client.send_to_session.ok", "delivered", delivered)
	return delivered, nil
}

// TailTranscriptViaDaemon opens the daemon's transcript stream for
// the given session id. The returned channel closes when the stream
// terminates. Calling cancel stops the subscription.
func TailTranscriptViaDaemon(parent context.Context, sessionID string, startOffset int64) (<-chan *clydev1.TailTranscriptResponse, context.CancelFunc, error) {
	log := daemonClientLog(parent)
	log.DebugContext(parent, "daemon.client.tail_transcript.begin",
		"session_id", sessionID,
		"start_offset", startOffset,
	)
	c, err := ConnectOrStart(parent)
	if err != nil {
		log.DebugContext(parent, "daemon.client.tail_transcript.connect_failed", "err", err)
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parent)
	stream, err := c.rpc.TailTranscript(ctx, &clydev1.TailTranscriptRequest{
		SessionId:     sessionID,
		StartAtOffset: startOffset,
	})
	if err != nil {
		log.DebugContext(ctx, "daemon.client.tail_transcript.stream_open_failed", "err", err)
		cancel()
		c.conn.Close()
		return nil, nil, err
	}
	log.DebugContext(ctx, "daemon.client.tail_transcript.stream_open")
	out := make(chan *clydev1.TailTranscriptResponse, 64)
	go func() {
		defer close(out)
		defer c.conn.Close()
		loopLog := daemonClientLog(ctx)
		for {
			line, err := stream.Recv()
			if err != nil {
				loopLog.DebugContext(ctx, "daemon.client.tail_transcript.recv_done", "err", err)
				return
			}
			select {
			case out <- line:
			case <-ctx.Done():
				loopLog.DebugContext(ctx, "daemon.client.tail_transcript.consumer_cancelled")
				return
			}
		}
	}()
	return out, cancel, nil
}

const launchAgentLabel = "io.goodkind.clyde.daemon"

// startDaemon starts the daemon process. On macOS, tries launchctl kickstart
// first (if the LaunchAgent is registered), falling back to direct spawn.
func startDaemon() error {
	bg := context.Background()
	log := daemonClientLog(bg)
	if runtime.GOOS == "darwin" {
		uid := strconv.Itoa(os.Getuid())
		target := "gui/" + uid + "/" + launchAgentLabel
		if err := exec.Command("launchctl", "kickstart", target).Run(); err == nil {
			log.DebugContext(bg, "daemon.client.start_daemon.kickstart_ok", "target", target)
			return nil // launchd started it
		}
		log.DebugContext(bg, "daemon.client.start_daemon.kickstart_failed_try_direct", "target", target)
		// launchctl failed (agent not registered)  --  fall through to direct spawn
	}

	return spawnDaemonDirect()
}

// spawnDaemonDirect starts the daemon as a detached child process.
func spawnDaemonDirect() error {
	bg := context.Background()
	log := daemonClientLog(bg)
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own path: %w", err)
	}

	daemonCmd := exec.Command(self, "daemon")
	daemonCmd.Stdout = nil
	daemonCmd.Stderr = nil
	daemonCmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}

	if err := daemonCmd.Start(); err != nil {
		log.DebugContext(bg, "daemon.client.spawn_direct.start_failed", "err", err)
		return err
	}
	log.DebugContext(bg, "daemon.client.spawn_direct.started", "pid", daemonCmd.Process.Pid)
	go func() { _ = daemonCmd.Wait() }()
	return nil
}
