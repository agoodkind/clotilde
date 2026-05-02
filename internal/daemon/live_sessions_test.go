package daemon

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/codex"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/clyde/internal/session"
)

func TestLiveSessionLaunchBasedirRequiresExplicitOrStoredDirectory(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))

	explicit := filepath.Join(tmp, "explicit")
	got, err := liveSessionLaunchBasedir("", explicit)
	if err != nil {
		t.Fatalf("explicit basedir: %v", err)
	}
	if got != explicit {
		t.Fatalf("explicit basedir = %q want %q", got, explicit)
	}

	if _, err := liveSessionLaunchBasedir("", ""); err == nil {
		t.Fatalf("missing basedir returned nil error")
	} else if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("missing basedir code = %v", status.Code(err))
	}

	store, err := session.NewGlobalFileStore()
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	stored := session.NewSession("stored-chat", "session-id")
	stored.Metadata.WorkDir = filepath.Join(tmp, "stored-workdir")
	if err := store.Create(stored); err != nil {
		t.Fatalf("create stored session: %v", err)
	}
	got, err = liveSessionLaunchBasedir("stored-chat", "")
	if err != nil {
		t.Fatalf("stored basedir: %v", err)
	}
	if got != stored.Metadata.WorkDir {
		t.Fatalf("stored basedir = %q want %q", got, stored.Metadata.WorkDir)
	}
}

func TestForegroundLeaseSuspendsAndRestoresCodexLiveRuntime(t *testing.T) {
	tmp := setupDaemonTestHome(t)
	store, err := session.NewGlobalFileStore()
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	sess := session.NewSession("codex-chat", "codex-thread")
	setTestProviderIdentity(sess, session.ProviderCodex, "codex-thread")
	sess.Metadata.WorkDir = filepath.Join(tmp, "work")
	if err := store.Create(sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := newTestServer(t)
	startRuntime := &fakeLiveRuntime{}
	restoreRuntime := &fakeLiveRuntime{attachSession: &codex.LiveSession{ThreadID: "codex-thread", WorkDir: sess.Metadata.WorkDir, Model: "gpt-test"}}
	oldFactory := newCodexLiveRuntime
	factoryCalls := 0
	newCodexLiveRuntime = func(codex.LiveRuntimeOptions) codex.LiveRuntime {
		factoryCalls++
		return restoreRuntime
	}
	defer func() { newCodexLiveRuntime = oldFactory }()

	srv.liveSessions["codex-thread"] = &liveRuntimeSession{
		provider:     session.ProviderCodex,
		name:         "codex-chat",
		id:           "codex-thread",
		basedir:      sess.Metadata.WorkDir,
		model:        "gpt-test",
		status:       "idle",
		startedAt:    daemonNow(),
		codexRuntime: startRuntime,
	}

	acquired, err := srv.AcquireForegroundSession(context.Background(), &clydev1.AcquireForegroundSessionRequest{
		SessionName: "codex-chat",
		Provider:    string(session.ProviderCodex),
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired.GetShouldRestore() {
		t.Fatalf("should_restore = false, want true")
	}
	if !startRuntime.closed {
		t.Fatalf("start runtime was not closed")
	}
	if _, ok := srv.liveSessions["codex-thread"]; ok {
		t.Fatalf("codex live session still present after acquire")
	}

	released, err := srv.ReleaseForegroundSession(context.Background(), &clydev1.ReleaseForegroundSessionRequest{
		LeaseToken: acquired.GetLeaseToken(),
		ExitState:  "ok",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released.GetRestored() {
		t.Fatalf("restored = false, want true")
	}
	if factoryCalls != 1 {
		t.Fatalf("factory calls = %d, want 1", factoryCalls)
	}
	if restoreRuntime.attachedThread != "codex-thread" {
		t.Fatalf("attached thread = %q, want codex-thread", restoreRuntime.attachedThread)
	}
	if _, ok := srv.liveSessions["codex-thread"]; !ok {
		t.Fatalf("codex live session not restored")
	}
}

func TestForegroundLeaseNoopsWhenNoLiveRuntimeExists(t *testing.T) {
	setupDaemonTestHome(t)
	store, err := session.NewGlobalFileStore()
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	sess := session.NewSession("codex-chat", "codex-thread")
	setTestProviderIdentity(sess, session.ProviderCodex, "codex-thread")
	if err := store.Create(sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := newTestServer(t)

	acquired, err := srv.AcquireForegroundSession(context.Background(), &clydev1.AcquireForegroundSessionRequest{
		SessionName: "codex-chat",
		Provider:    string(session.ProviderCodex),
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if acquired.GetShouldRestore() {
		t.Fatalf("should_restore = true, want false")
	}
	released, err := srv.ReleaseForegroundSession(context.Background(), &clydev1.ReleaseForegroundSessionRequest{LeaseToken: acquired.GetLeaseToken()})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if released.GetRestored() {
		t.Fatalf("restored = true, want false")
	}
}

func TestForegroundLeaseSuspendsAndRestoresClaudeRemoteWorker(t *testing.T) {
	tmp := setupDaemonTestHome(t)
	store, err := session.NewGlobalFileStore()
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	basedir := filepath.Join(tmp, "work")
	if err := os.MkdirAll(basedir, 0o755); err != nil {
		t.Fatalf("mkdir basedir: %v", err)
	}
	sess := session.NewSession("claude-chat", "claude-session")
	setTestProviderIdentity(sess, session.ProviderClaude, "claude-session")
	sess.Metadata.WorkDir = basedir
	if err := store.Create(sess); err != nil {
		t.Fatalf("create session: %v", err)
	}
	srv := newTestServer(t)
	srv.remoteMu.Lock()
	srv.remoteWorkers["claude-chat"] = &remoteWorker{
		sessionName: "claude-chat",
		sessionID:   "claude-session",
		incognito:   true,
	}
	srv.remoteMu.Unlock()
	script := filepath.Join(tmp, "fake-worker.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write fake worker: %v", err)
	}
	oldExec := remoteWorkerExecutable
	remoteWorkerExecutable = func() (string, error) { return script, nil }
	defer func() { remoteWorkerExecutable = oldExec }()

	acquired, err := srv.AcquireForegroundSession(context.Background(), &clydev1.AcquireForegroundSessionRequest{
		SessionName: "claude-chat",
		Provider:    string(session.ProviderClaude),
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !acquired.GetShouldRestore() {
		t.Fatalf("should_restore = false, want true")
	}
	srv.remoteMu.Lock()
	_, stillPresent := srv.remoteWorkers["claude-chat"]
	srv.remoteMu.Unlock()
	if stillPresent {
		t.Fatalf("remote worker still present after acquire")
	}

	released, err := srv.ReleaseForegroundSession(context.Background(), &clydev1.ReleaseForegroundSessionRequest{
		LeaseToken: acquired.GetLeaseToken(),
		ExitState:  "ok",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if !released.GetRestored() {
		t.Fatalf("restored = false, want true")
	}
	srv.remoteMu.Lock()
	restored := srv.remoteWorkers["claude-chat"]
	srv.remoteMu.Unlock()
	if restored == nil || restored.sessionID != "claude-session" {
		t.Fatalf("remote worker not restored: %#v", restored)
	}
	if !restored.incognito {
		t.Fatalf("restored incognito = false, want true")
	}
	if restored.cmd != nil && restored.cmd.Process != nil {
		_ = restored.cmd.Process.Kill()
	}
	if restored.done != nil {
		<-restored.done
	}
}

func TestReleaseForegroundSessionIsIdempotent(t *testing.T) {
	setupDaemonTestHome(t)
	srv := newTestServer(t)
	resp, err := srv.ReleaseForegroundSession(context.Background(), &clydev1.ReleaseForegroundSessionRequest{
		LeaseToken: "missing",
	})
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if resp.GetRestored() {
		t.Fatalf("restored = true, want false")
	}
}

func setupDaemonTestHome(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmp, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))
	t.Setenv("XDG_RUNTIME_DIR", filepath.Join(tmp, "run"))
	return tmp
}

func newTestServer(t *testing.T) *Server {
	t.Helper()
	srv, err := New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(srv.Close)
	return srv
}

func setTestProviderIdentity(sess *session.Session, provider session.ProviderID, id string) {
	sess.Metadata.Provider = provider
	sess.Metadata.SessionID = id
	sess.Metadata.ProviderState = &session.ProviderOwnedMetadata{
		Current: session.ProviderSessionID{Provider: provider, ID: id},
	}
	sess.Metadata.NormalizeProviderState()
}

type fakeLiveRuntime struct {
	closed         bool
	attachedThread string
	attachSession  *codex.LiveSession
}

func (f *fakeLiveRuntime) Start(context.Context, codex.LiveStartRequest) (*codex.LiveSession, error) {
	return nil, nil
}

func (f *fakeLiveRuntime) Attach(_ context.Context, req codex.LiveAttachRequest) (*codex.LiveSession, error) {
	f.attachedThread = req.ThreadID
	if f.attachSession != nil {
		return f.attachSession, nil
	}
	return &codex.LiveSession{ThreadID: req.ThreadID}, nil
}

func (f *fakeLiveRuntime) Send(context.Context, codex.LiveSendRequest) (*codex.LiveTurn, error) {
	return nil, nil
}

func (f *fakeLiveRuntime) Stream(context.Context, codex.LiveStreamRequest) (<-chan codex.LiveEvent, error) {
	return nil, nil
}

func (f *fakeLiveRuntime) Stop(context.Context, codex.LiveStopRequest) error {
	return nil
}

func (f *fakeLiveRuntime) Close() error {
	f.closed = true
	return nil
}
