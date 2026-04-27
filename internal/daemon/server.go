// Package daemon implements the clyde daemon gRPC server.
// It manages per-session settings.json files so that /model changes
// in one Claude session don't leak to others. The daemon is lazily
// started on first use and exits after an idle timeout.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	"goodkind.io/clyde/internal/bridge"
	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/outputstyle"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/sessionctx"
	"goodkind.io/clyde/internal/util"
)

// Server implements the Clyde gRPC service.
type Server struct {
	clydev1.UnimplementedClydeServiceServer

	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher        *fsnotify.Watcher
	bridgeWatcher  *bridge.Watcher
	globalSettings map[string]json.RawMessage // last-known global settings.json content

	// scanWake fires the discovery scanner immediately. Buffered so a
	// trigger never blocks the caller.
	scanWake chan discoveryScanSignal

	// subscribers receive registry events as they happen. The mutex
	// guards the map so subscribe and broadcast can run concurrently.
	subMu       sync.Mutex
	subscribers map[chan *clydev1.SubscribeRegistryResponse]registrySubscriberState

	// settingsLocks serialises writes per session name so two callers
	// updating the same session do not stomp on each other. The lock
	// for a session is created lazily and lives for the daemon's
	// lifetime; the cardinality is bounded by the number of sessions.
	settingsLocksMu sync.Mutex
	settingsLocks   map[string]*sync.Mutex

	// bridges maps Claude session UUIDs to the bridge URL exposed by
	// `claude --remote-control`. Populated by the bridge watcher.
	bridgeMu sync.RWMutex
	bridges  map[string]*clydev1.Bridge

	// transcripts hub fans tail lines out to multiple subscribers.
	transcripts   *transcriptHub
	providerStats *providerStatsHub

	remoteMu      sync.Mutex
	remoteWorkers map[string]*remoteWorker

	contextMu         sync.Mutex
	contextStates     map[string]sessionContextState
	contextRefreshSem chan contextRefreshPermit

	reloadMu sync.Mutex
	reloadFn func(context.Context) (reloadReport, error)

	skipRuntimeCleanup atomic.Bool
}

// wrapperSession holds runtime state for one active claude wrapper process.
type wrapperSession struct {
	wrapperID   string
	sessionName string // empty for bare claude invocations
	model       string
	effortLevel string
}

type remoteWorker struct {
	sessionName string
	sessionID   string
	incognito   bool
	cmd         *exec.Cmd
}

var remoteWorkerExecutable = os.Executable

type reloadReport struct {
	BinaryReloaded bool
	NewPID         int
}

type discoveryScanSignal struct {
	Requested bool
}

type registrySubscriberState bool

type contextRefreshPermit struct {
	Acquired bool
}

type sessionContextState struct {
	Usage      sessionctx.Usage
	Loaded     bool
	Status     string
	Refreshing bool
	RetryAfter time.Time
}

func (s *Server) SetReloadFunc(fn func(context.Context) (reloadReport, error)) {
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()
	s.reloadFn = fn
}

func (s *Server) preserveRuntimeDirsOnClose() {
	s.skipRuntimeCleanup.Store(true)
}

// New creates a new daemon Server and starts watching the global settings file.
func New(log *slog.Logger) (*Server, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create settings watcher: %w", err)
	}

	s := &Server{
		log:               log,
		sessions:          make(map[string]*wrapperSession),
		watcher:           watcher,
		bridgeWatcher:     nil,
		scanWake:          make(chan discoveryScanSignal, 4),
		subscribers:       make(map[chan *clydev1.SubscribeRegistryResponse]registrySubscriberState),
		settingsLocks:     make(map[string]*sync.Mutex),
		bridges:           make(map[string]*clydev1.Bridge),
		transcripts:       newTranscriptHub(),
		providerStats:     newProviderStatsHub(log),
		remoteWorkers:     make(map[string]*remoteWorker),
		contextStates:     make(map[string]sessionContextState),
		contextRefreshSem: make(chan contextRefreshPermit, 2),
	}

	globalPath := globalSettingsPath()
	if err := s.loadGlobalSettings(); err != nil {
		log.LogAttrs(context.Background(), slog.LevelWarn, "global settings load failed on startup",
			slog.String("path", globalPath),
			slog.Any("err", err),
		)
	} else {
		globalModel := globalSettingString(s.globalSettings, "model")
		log.LogAttrs(context.Background(), slog.LevelInfo, "global settings loaded",
			slog.String("path", globalPath),
			slog.String("model", globalModel),
			slog.Int("keys", len(s.globalSettings)),
		)
	}

	if err := watcher.Add(globalPath); err != nil {
		log.LogAttrs(context.Background(), slog.LevelWarn, "failed to watch global settings",
			slog.String("path", globalPath),
			slog.Any("err", err),
		)
	} else {
		log.LogAttrs(context.Background(), slog.LevelInfo, "watching global settings",
			slog.String("path", globalPath),
		)
	}

	go s.watchGlobalSettings()
	go s.runDiscoveryScanner()

	if home, err := os.UserHomeDir(); err == nil {
		sessionsDir := filepath.Join(home, ".claude", "sessions")
		if w, err := bridge.Start(sessionsDir); err == nil {
			s.bridgeWatcher = w
			go s.runBridgeWatcher(w)
			s.log.LogAttrs(context.Background(), slog.LevelInfo, "bridge watcher started",
				slog.String("dir", sessionsDir),
			)
		} else {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "bridge watcher failed",
				slog.Any("err", err),
			)
		}
	}

	return s, nil
}

// runBridgeWatcher consumes bridge events from the watcher and
// translates them into SubscribeRegistryResponse broadcasts.
func (s *Server) runBridgeWatcher(w *bridge.Watcher) {
	// Seed the cache with anything the watcher already saw on its
	// initial scan so ListBridges callers do not race the first event.
	for _, b := range w.Snapshot() {
		s.setBridge(&clydev1.Bridge{
			SessionId:       b.SessionID,
			Pid:             b.PID,
			BridgeSessionId: b.BridgeSessionID,
			Url:             b.URL,
		})
	}
	for ev := range w.Events() {
		switch ev.Kind {
		case bridge.EventOpened:
			s.setBridge(&clydev1.Bridge{
				SessionId:       ev.Bridge.SessionID,
				Pid:             ev.Bridge.PID,
				BridgeSessionId: ev.Bridge.BridgeSessionID,
				Url:             ev.Bridge.URL,
			})
		case bridge.EventClosed:
			s.removeBridge(ev.Bridge.SessionID)
		}
	}
}

// runDiscoveryScanner periodically walks ~/.claude/projects and adopts
// any transcripts whose UUID is not already tracked by clyde. Runs
// once at startup, then on a 5 minute cadence. The scanner wakes
// early when a SIGUSR1 lands or when a TriggerScan RPC arrives so
// clients can refresh the daemon's view immediately. Errors are logged
// but do not stop the loop.
func (s *Server) runDiscoveryScanner() {
	const interval = 5 * time.Minute
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGUSR1)

	for {
		s.runDiscoveryOnce()
		select {
		case <-time.After(interval):
		case <-sig:
			s.log.LogAttrs(context.Background(), slog.LevelDebug, "discovery scan wake from SIGUSR1")
		case <-s.scanWake:
			s.log.LogAttrs(context.Background(), slog.LevelDebug, "discovery scan wake from TriggerScan RPC")
		}
	}
}

func (s *Server) runDiscoveryOnce() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	projects := config.ClaudeProjectsRoot(home)
	if _, err := os.Stat(projects); err != nil {
		return
	}
	results, err := session.ScanProjects(projects)
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelWarn, "discovery scan failed",
			slog.Any("err", err),
		)
		return
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelWarn, "discovery store init failed",
			slog.Any("err", err),
		)
		return
	}
	adopted, err := session.AdoptUnknown(store, results)
	if err != nil {
		s.log.LogAttrs(context.Background(), slog.LevelWarn, "discovery adopt failed",
			slog.Any("err", err),
		)
		return
	}
	if len(adopted) > 0 {
		names := make([]string, 0, len(adopted))
		for _, a := range adopted {
			names = append(names, a.Name)
			sess := &session.Session{Name: a.Name, Metadata: a.Metadata}
			s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_ADOPTED, store, sess, "")
		}
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "discovery adopted sessions",
			slog.Int("count", len(adopted)),
			slog.Any("names", names),
		)
	}
}

// TriggerScan implements the RPC. The daemon's scanner runs whenever
// the request lands; the response carries any sessions adopted by the
// previous scan tick so the caller has immediate confirmation.
// Subscribers also receive a SESSION_ADOPTED event for each new entry.
func (s *Server) TriggerScan(ctx context.Context, _ *clydev1.TriggerScanRequest) (*clydev1.TriggerScanResponse, error) {
	select {
	case s.scanWake <- discoveryScanSignal{Requested: true}:
	default:
		// Channel is full; another wake is already pending.
	}
	return &clydev1.TriggerScanResponse{}, nil
}

// SubscribeRegistry streams SubscribeRegistryResponse values to the client until
// the client disconnects. Each subscriber gets its own buffered channel
// so a slow client cannot block the broadcaster. Events that arrive
// while a subscriber's buffer is full are dropped for that one client.
func (s *Server) SubscribeRegistry(_ *clydev1.SubscribeRegistryRequest, stream clydev1.ClydeService_SubscribeRegistryServer) error {
	ch := make(chan *clydev1.SubscribeRegistryResponse, 32)

	s.subMu.Lock()
	s.subscribers[ch] = true
	s.subMu.Unlock()

	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, ch)
		s.subMu.Unlock()
		close(ch)
	}()

	ctx := stream.Context()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (s *Server) GetProviderStats(ctx context.Context, _ *clydev1.GetProviderStatsRequest) (*clydev1.GetProviderStatsResponse, error) {
	resp := &clydev1.GetProviderStatsResponse{
		Providers:    s.providerStats.snapshot(),
		LoadedAtUnix: s.providerStats.loadedAtUnix(),
	}
	s.log.DebugContext(ctx, "provider_stats.snapshot_served",
		"component", "daemon",
		"providers", len(resp.Providers),
	)
	return resp, nil
}

func (s *Server) SubscribeProviderStats(_ *clydev1.SubscribeProviderStatsRequest, stream clydev1.ClydeService_SubscribeProviderStatsServer) error {
	ch := s.providerStats.subscribe()
	defer s.providerStats.unsubscribe(ch)

	ctx := stream.Context()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// RenameSession is the daemon-side rename. The daemon owns the rename
// so that no other process can simultaneously mutate the registry
// while the rename is in flight. A SESSION_RENAMED event broadcasts
// the change to every subscriber.
func (s *Server) RenameSession(ctx context.Context, req *clydev1.RenameSessionRequest) (*clydev1.RenameSessionResponse, error) {
	if req.OldName == "" || req.NewName == "" {
		return nil, status.Error(codes.InvalidArgument, "old_name and new_name are required")
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	if err := store.Rename(req.OldName, req.NewName); err != nil {
		return nil, status.Errorf(codes.Internal, "rename failed: %v", err)
	}
	s.renameContextState(req.OldName, req.NewName)
	renamed, _ := store.Get(req.NewName)
	s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_RENAMED, store, renamed, req.OldName)
	s.log.LogAttrs(ctx, slog.LevelInfo, "session renamed via RPC",
		slog.String("old", req.OldName),
		slog.String("new", req.NewName),
	)
	return &clydev1.RenameSessionResponse{}, nil
}

// DeleteSession is the daemon-side delete. It removes registry metadata,
// Claude transcripts, agent logs, and per-session output style artifacts,
// then broadcasts SESSION_DELETED so dashboards prune the row immediately.
func (s *Server) DeleteSession(ctx context.Context, req *clydev1.DeleteSessionRequest) (*clydev1.DeleteSessionResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Resolve(req.Name)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve session: %v", err)
	}
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.Name)
	}
	projClydeRoot := daemonProjectClydeRootForSession(sess)
	if _, err := daemonDeleteSessionData(projClydeRoot, sess.Metadata.SessionID, sess.Metadata.TranscriptPath); err != nil {
		s.log.Warn("daemon.session_delete.current_data_failed",
			"component", "daemon",
			"subcomponent", "sessions",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"err", err)
	}
	for _, prevID := range sess.Metadata.PreviousSessionIDs {
		if _, err := daemonDeleteSessionData(projClydeRoot, prevID, ""); err != nil {
			s.log.Warn("daemon.session_delete.previous_data_failed",
				"component", "daemon",
				"subcomponent", "sessions",
				"session", sess.Name,
				"previous_session_id", prevID,
				"err", err)
		}
	}
	if sess.Metadata.HasCustomOutputStyle {
		if err := outputstyle.DeleteCustomStyleFile(config.GlobalOutputStyleRoot(), sess.Name); err != nil {
			s.log.Warn("daemon.session_delete.output_style_failed",
				"component", "daemon",
				"subcomponent", "sessions",
				"session", sess.Name,
				"err", err)
		}
	}
	if err := store.Delete(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "delete failed: %v", err)
	}
	s.deleteContextState(req.Name)
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:        clydev1.SubscribeRegistryResponse_KIND_SESSION_DELETED,
		SessionName: req.Name,
	})
	s.log.LogAttrs(ctx, slog.LevelInfo, "session deleted via RPC",
		slog.String("name", req.Name),
	)
	return &clydev1.DeleteSessionResponse{}, nil
}

// UpdateSessionMetadata is the daemon-owned write path for session metadata
// fields that the TUI can edit in-place (for now: workspace root).
func (s *Server) UpdateSessionMetadata(ctx context.Context, req *clydev1.UpdateSessionMetadataRequest) (*clydev1.UpdateSessionMetadataResponse, error) {
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Get(req.GetName())
	if err != nil || sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetName())
	}
	sess.Metadata.WorkspaceRoot = strings.TrimSpace(req.GetWorkspaceRoot())
	if err := store.Update(sess); err != nil {
		return nil, status.Errorf(codes.Internal, "update session metadata: %v", err)
	}
	s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_UPDATED, store, sess, "")
	s.log.LogAttrs(ctx, slog.LevelInfo, "session metadata updated via RPC",
		slog.String("session", sess.Name),
		slog.String("workspace_root", sess.Metadata.WorkspaceRoot),
	)
	return &clydev1.UpdateSessionMetadataResponse{}, nil
}

func daemonProjectClydeRootForSession(sess *session.Session) string {
	root := sess.Metadata.WorkspaceRoot
	if root == "" {
		root, _ = config.FindProjectRoot()
	}
	return filepath.Join(root, config.ClydeDir)
}

// publishEvent fans an event out to every active subscriber. Slow
// subscribers whose buffer is full silently drop the event to keep the
// broadcaster non-blocking.
func (s *Server) publishEvent(ev *clydev1.SubscribeRegistryResponse) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (s *Server) publishSessionSummaryEvent(kind clydev1.SubscribeRegistryResponse_Kind, store *session.FileStore, sess *session.Session, oldName string) {
	if sess == nil {
		return
	}
	ev := &clydev1.SubscribeRegistryResponse{
		Kind:        kind,
		SessionName: sess.Name,
		SessionId:   sess.Metadata.SessionID,
		OldName:     oldName,
	}
	if store != nil {
		ev.SessionSummary = s.sessionSummary(store, sess)
	}
	s.publishEvent(ev)
}

func (s *Server) publishGlobalSettingsEvent(globalRemoteControl bool) {
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:                clydev1.SubscribeRegistryResponse_KIND_GLOBAL_SETTINGS_UPDATED,
		GlobalRemoteControl: globalRemoteControl,
	})
}

// Close shuts down the watcher and cleans up all active session runtime dirs.
func (s *Server) Close() {
	bridge.Close(s.bridgeWatcher)
	if s.watcher != nil {
		_ = s.watcher.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		if !s.skipRuntimeCleanup.Load() {
			_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
		}
	}
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "daemon closed",
		slog.Int("cleaned_sessions", len(s.sessions)),
		slog.Bool("preserved_runtime_dirs", s.skipRuntimeCleanup.Load()),
	)
}

// AcquireSession writes a per-session settings.json (global settings with
// model overridden) and returns the path along with the real claude binary.
func (s *Server) AcquireSession(ctx context.Context, req *clydev1.AcquireSessionRequest) (*clydev1.AcquireSessionResponse, error) {
	if req.WrapperId == "" || req.WrapperId == "__probe__" {
		return nil, status.Error(codes.InvalidArgument, "wrapper_id is required")
	}

	// Check if this session already has a settings file on disk (re-acquire
	// after daemon restart). Preserve its current model/effort rather than
	// resetting to global defaults.
	existingModel, existingEffort := s.readSessionSettings(req.WrapperId)

	var model, effortLevel string
	if existingModel != "" || existingEffort != "" {
		// Re-registering after daemon restart  --  keep what claude has.
		model = existingModel
		effortLevel = existingEffort
		s.log.LogAttrs(ctx, slog.LevelInfo, "re-acquired session with preserved settings",
			slog.String("wrapper_id", req.WrapperId),
			slog.String("model", model),
			slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(model)),
			slog.String("effort", effortLevel),
		)
	} else {
		// Fresh session  --  resolve from clyde session settings + global.
		model, effortLevel = s.resolveSessionSettings(req.SessionName)
	}

	settingsFile, err := s.writeSettingsJSON(req.WrapperId, model, effortLevel)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to write settings: %v", err)
	}

	sess := &wrapperSession{
		wrapperID:   req.WrapperId,
		sessionName: req.SessionName,
		model:       model,
		effortLevel: effortLevel,
	}

	s.mu.Lock()
	s.sessions[req.WrapperId] = sess
	s.mu.Unlock()

	realClaude, err := findRealClaude()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find real claude binary: %v", err)
	}

	s.log.LogAttrs(ctx, slog.LevelInfo, "session acquired",
		slog.String("wrapper_id", req.WrapperId),
		slog.String("session", req.SessionName),
		slog.String("model", model),
		slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(model)),
		slog.String("settings_file", settingsFile),
		slog.String("claude_bin", realClaude),
		slog.Int("active_sessions", len(s.sessions)),
	)

	return &clydev1.AcquireSessionResponse{
		RealClaude:   realClaude,
		Model:        model,
		SettingsFile: settingsFile,
	}, nil
}

// ReleaseSession removes the per-session runtime dir after claude exits.
// When the last session is released, the idle timer starts.
func (s *Server) ReleaseSession(ctx context.Context, req *clydev1.ReleaseSessionRequest) (*clydev1.ReleaseSessionResponse, error) {
	s.mu.Lock()
	sess, ok := s.sessions[req.WrapperId]
	if ok {
		delete(s.sessions, req.WrapperId)
	}
	remaining := len(s.sessions)
	s.mu.Unlock()

	if ok {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
		s.log.LogAttrs(ctx, slog.LevelInfo, "session released",
			slog.String("wrapper_id", req.WrapperId),
			slog.String("session", sess.sessionName),
			slog.String("model", sess.model),
			slog.Int("active_sessions", remaining),
		)
	} else {
		s.log.LogAttrs(ctx, slog.LevelWarn, "release for unknown session",
			slog.String("wrapper_id", req.WrapperId),
		)
	}

	return &clydev1.ReleaseSessionResponse{}, nil
}

// hookEventPayload is the JSON structure sent via HookEvent RPC.
type hookEventPayload struct {
	Type          string   `json:"type"`
	SessionName   string   `json:"session_name,omitempty"`
	WorkspaceRoot string   `json:"workspace_root,omitempty"`
	Messages      []string `json:"messages,omitempty"` // pre-extracted recent messages
}

// HookEvent processes a Claude Code hook event forwarded from a wrapper process.
func (s *Server) HookEvent(ctx context.Context, req *clydev1.HookEventRequest) (*clydev1.HookEventResponse, error) {
	var payload hookEventPayload
	if err := json.Unmarshal(req.RawJson, &payload); err != nil {
		return &clydev1.HookEventResponse{ExitCode: 0}, nil
	}

	switch payload.Type {
	case "update_context":
		// Stubbed. The previous implementation shelled out to
		// `claude -p --model sonnet` which recursed through the
		// SessionStart hook chain (claude -> clyde hook -> daemon
		// -> claude -p ...) and fanned out a process tree until the
		// host ran out of file descriptors. The whole subsystem is
		// disabled until it is rebuilt against the in-process
		// adapter. See config.LabelerConfig for the future wiring
		// point.
		// Intentionally silent to avoid noisy steady-state daemon logs.
	}

	return &clydev1.HookEventResponse{ExitCode: 0}, nil
}

// adapterScratchDir returns the cwd used for OpenAI adapter spawned
// claude -p calls. The path is created lazily and cached.
var (
	adapterScratchOnce sync.Once
	adapterScratchPath string
)

func adapterScratchDir() string {
	adapterScratchOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			return
		}
		dir := filepath.Join(base, "clyde", "adapter-scratch")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		adapterScratchPath = dir
	})
	return adapterScratchPath
}

// writeSettingsJSON writes a per-session settings.json to the runtime dir,
// merging global settings with the per-session model override.
func (s *Server) writeSettingsJSON(wrapperID, model, effortLevel string) (string, error) {
	s.mu.RLock()
	globalCopy := make(map[string]json.RawMessage, len(s.globalSettings))
	for k, v := range s.globalSettings {
		globalCopy[k] = v
	}
	globalModel := globalSettingString(s.globalSettings, "model")
	s.mu.RUnlock()

	model = adaptercursor.NormalizeSessionSettingsModel(model)
	if model != "" {
		if encoded, err := json.Marshal(model); err == nil {
			globalCopy["model"] = encoded
		}
	}
	if effortLevel != "" {
		if encoded, err := json.Marshal(effortLevel); err == nil {
			globalCopy["effortLevel"] = encoded
		}
	}

	effectiveModel := globalSettingString(globalCopy, "model")
	s.log.LogAttrs(context.Background(), slog.LevelDebug, "writing per-session settings",
		slog.String("wrapper_id", wrapperID),
		slog.String("global_model", globalModel),
		slog.String("session_model", model),
		slog.String("cursor_normalized_session_model", adaptercursor.NormalizeModelAlias(model)),
		slog.String("session_effort", effortLevel),
		slog.String("effective_model", effectiveModel),
		slog.Int("settings_keys", len(globalCopy)),
	)

	data, err := json.MarshalIndent(globalCopy, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal settings: %w", err)
	}

	sessionDir := config.SessionRuntimeDir(wrapperID)
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return "", fmt.Errorf("failed to create session dir: %w", err)
	}

	settingsPath := filepath.Join(sessionDir, "settings.json")
	if err := os.WriteFile(settingsPath, data, 0o600); err != nil {
		return "", fmt.Errorf("failed to write settings.json: %w", err)
	}

	return settingsPath, nil
}

// syncAllSessions rewrites settings.json for all active sessions when the
// global settings file changes. Each session's current model is preserved
// so that /model changes in one session don't leak to others.
func (s *Server) syncAllSessions() {
	s.mu.RLock()
	sessions := make([]*wrapperSession, 0, len(s.sessions))
	for _, sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.RUnlock()

	for _, sess := range sessions {
		currentModel, currentEffort := s.readSessionSettings(sess.wrapperID)
		if currentModel != "" {
			sess.model = currentModel
		}
		if currentEffort != "" {
			sess.effortLevel = currentEffort
		}
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "syncing session",
			slog.String("wrapper_id", sess.wrapperID),
			slog.String("session", sess.sessionName),
			slog.String("preserved_model", sess.model),
			slog.String("preserved_effort", sess.effortLevel),
		)
		if _, err := s.writeSettingsJSON(sess.wrapperID, sess.model, sess.effortLevel); err != nil {
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "failed to sync settings",
				slog.String("wrapper_id", sess.wrapperID),
				slog.Any("err", err),
			)
		}
	}

	s.log.LogAttrs(context.Background(), slog.LevelInfo, "global settings synced to all sessions",
		slog.Int("active_sessions", len(sessions)),
	)
}

// readSessionSettings reads model and effortLevel from a session's current settings.json.
// Returns "" for either value if the file doesn't exist or lacks the field.
func (s *Server) readSessionSettings(wrapperID string) (model, effortLevel string) {
	path := filepath.Join(config.SessionRuntimeDir(wrapperID), "settings.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return "", ""
	}
	model, _ = settings["model"].(string)
	model = adaptercursor.NormalizeSessionSettingsModel(model)
	effortLevel, _ = settings["effortLevel"].(string)
	return model, effortLevel
}

// watchGlobalSettings runs in a goroutine, syncing global settings changes
// to all active sessions.
func (s *Server) watchGlobalSettings() {
	for {
		select {
		case event, ok := <-s.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				s.log.LogAttrs(context.Background(), slog.LevelDebug, "global settings file changed",
					slog.String("event", event.Op.String()),
				)
				if err := s.reloadGlobalSettings(context.Background()); err != nil {
					s.log.LogAttrs(context.Background(), slog.LevelWarn, "failed to reload global settings",
						slog.Any("err", err),
					)
					continue
				}
			}

		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			s.log.LogAttrs(context.Background(), slog.LevelWarn, "settings watcher error",
				slog.Any("err", err),
			)
		}
	}
}

func (s *Server) reloadGlobalSettings(ctx context.Context) error {
	if err := s.loadGlobalSettings(); err != nil {
		return err
	}
	s.syncAllSessions()
	s.log.LogAttrs(ctx, slog.LevelInfo, "daemon global settings reloaded",
		slog.Int("active_sessions", s.activeSessionCount()),
	)
	return nil
}

func (s *Server) activeSessionCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.sessions)
}

// ReloadDaemon starts a replacement daemon binary and then lets the
// current process drain. Existing accepted gRPC streams stay attached
// to this process until they finish; new clients connect to the
// replacement once it has rebound the runtime socket.
func (s *Server) ReloadDaemon(ctx context.Context, req *clydev1.ReloadDaemonRequest) (*clydev1.ReloadDaemonResponse, error) {
	start := time.Now()
	s.reloadMu.Lock()
	fn := s.reloadFn
	s.reloadMu.Unlock()

	if fn == nil {
		return nil, status.Error(codes.FailedPrecondition, "daemon reload is not available")
	}
	report, err := fn(ctx)
	if err != nil {
		if errors.Is(err, errReloadBeforeProcessLock) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
		return nil, status.Errorf(codes.Internal, "reload binary: %v", err)
	}
	active := s.activeSessionCount()
	s.log.LogAttrs(ctx, slog.LevelInfo, "daemon reload completed",
		slog.Int("active_sessions", active),
		slog.Bool("binary_reloaded", report.BinaryReloaded),
		slog.Int("new_pid", report.NewPID),
		slog.Int64("duration_ms", time.Since(start).Milliseconds()),
	)
	return &clydev1.ReloadDaemonResponse{
		ActiveSessions: int32(active),
		BinaryReloaded: report.BinaryReloaded,
		NewPid:         int64(report.NewPID),
	}, nil
}

// loadGlobalSettings reads ~/.claude/settings.json into memory.
func (s *Server) loadGlobalSettings() error {
	path := globalSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.log.LogAttrs(context.Background(), slog.LevelDebug, "global settings file not found, using empty",
				slog.String("path", path),
			)
			s.mu.Lock()
			s.globalSettings = make(map[string]json.RawMessage)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("failed to read global settings: %w", err)
	}

	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse global settings: %w", err)
	}

	model := globalSettingString(settings, "model")
	s.log.LogAttrs(context.Background(), slog.LevelDebug, "global settings reloaded",
		slog.String("model", model),
		slog.Int("keys", len(settings)),
	)

	s.mu.Lock()
	s.globalSettings = settings
	s.mu.Unlock()

	return nil
}

// resolveSessionSettings loads per-session model and effortLevel from the
// clyde session's settings.json, falling back to global settings for any
// field not set at the session level.
func (s *Server) resolveSessionSettings(sessionName string) (model, effortLevel string) {
	s.mu.RLock()
	globalModel := globalSettingString(s.globalSettings, "model")
	globalEffort := globalSettingString(s.globalSettings, "effortLevel")
	s.mu.RUnlock()

	model = globalModel
	model = adaptercursor.NormalizeSessionSettingsModel(model)
	effortLevel = globalEffort

	if sessionName == "" {
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "no session name, using global settings",
			slog.String("model", model),
			slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(model)),
			slog.String("effort", effortLevel),
		)
		return model, effortLevel
	}

	// Load session-specific settings from clyde's global store
	sessSettings := loadClydeSessionSettings(sessionName)
	if sessSettings != nil {
		if sessSettings.Model != "" {
			model = adaptercursor.NormalizeSessionSettingsModel(sessSettings.Model)
		}
		if sessSettings.EffortLevel != "" {
			effortLevel = sessSettings.EffortLevel
		}
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "resolved session settings",
			slog.String("session", sessionName),
			slog.String("model", model),
			slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(model)),
			slog.String("effort", effortLevel),
		)
	} else {
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "no clyde session settings, using global",
			slog.String("session", sessionName),
			slog.String("model", model),
			slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(model)),
			slog.String("effort", effortLevel),
		)
	}

	return model, effortLevel
}

func globalSettingString(settings map[string]json.RawMessage, key string) string {
	if len(settings) == 0 {
		return ""
	}
	raw, ok := settings[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var out string
	if err := json.Unmarshal(raw, &out); err != nil {
		return ""
	}
	return out
}

// loadClydeSessionSettings loads settings.json from the clyde global
// store for the given session name. Returns nil if not found.
func loadClydeSessionSettings(sessionName string) *session.Settings {
	sessionDir := config.GetSessionDir(config.GlobalDataDir(), sessionName)
	settingsPath := filepath.Join(sessionDir, "settings.json")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return nil
	}

	var settings session.Settings
	if err := json.Unmarshal(data, &settings); err != nil {
		return nil
	}
	return &settings
}

func globalSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.Getenv("HOME")
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// ListActiveSessions returns all currently acquired sessions.
func (s *Server) ListActiveSessions(_ context.Context, _ *clydev1.ListActiveSessionsRequest) (*clydev1.ListActiveSessionsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var active []*clydev1.ActiveSession
	for wid, sess := range s.sessions {
		active = append(active, &clydev1.ActiveSession{
			SessionName: sess.sessionName,
			WrapperId:   wid,
		})
	}

	return &clydev1.ListActiveSessionsResponse{Sessions: active}, nil
}

func (s *Server) sessionIsActive(sessionName string) bool {
	sessionName = strings.TrimSpace(sessionName)
	if sessionName == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, sess := range s.sessions {
		if sess != nil && sess.sessionName == sessionName {
			return true
		}
	}
	return false
}

func (s *Server) ListSessions(ctx context.Context, _ *clydev1.ListSessionsRequest) (*clydev1.ListSessionsResponse, error) {
	started := time.Now()
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sessions, err := store.List()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list sessions: %v", err)
	}
	out := make([]*clydev1.SessionSummary, 0, len(sessions))
	for _, sess := range sessions {
		out = append(out, s.sessionSummary(store, sess))
	}
	globalRC := false
	if cfg, err := config.LoadGlobalOrDefault(); err == nil {
		globalRC = cfg.Defaults.RemoteControl
	}
	s.log.Debug("daemon.sessions.list.completed",
		"component", "daemon",
		"subcomponent", "sessions",
		"duration_ms", time.Since(started).Milliseconds(),
		"sessions_total", len(out))
	return &clydev1.ListSessionsResponse{Sessions: out, GlobalRemoteControl: globalRC}, nil
}

func (s *Server) GetSessionDetail(ctx context.Context, req *clydev1.GetSessionDetailRequest) (*clydev1.GetSessionDetailResponse, error) {
	if req.GetSessionName() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_name is required")
	}
	started := time.Now()
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Resolve(req.GetSessionName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve session: %v", err)
	}
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionName())
	}
	detail := s.sessionDetail(store, sess)
	s.log.Debug("daemon.session_detail.completed",
		"component", "daemon",
		"subcomponent", "sessions",
		"duration_ms", time.Since(started).Milliseconds(),
		"session", sess.Name,
		"messages_total", detail.GetTotalMessages())
	return detail, nil
}

func (s *Server) contextStateForSession(sess *session.Session) sessionContextState {
	if sess == nil || sess.Name == "" || sess.Metadata.SessionID == "" || strings.TrimSpace(sess.Metadata.TranscriptPath) == "" {
		return sessionContextState{}
	}

	s.contextMu.Lock()
	state := s.contextStates[sess.Name]
	needsRefresh := false
	switch {
	case state.Refreshing:
	case !state.RetryAfter.IsZero() && time.Now().Before(state.RetryAfter):
	case !state.Loaded:
		needsRefresh = true
	case transcriptNewerThan(sess.Metadata.TranscriptPath, state.Usage.CapturedAt):
		needsRefresh = true
	}
	if needsRefresh {
		state.Refreshing = true
		if !state.Loaded {
			state.Status = "loading..."
		}
		s.contextStates[sess.Name] = state
		go s.refreshContextUsage(sess)
	}
	state = s.contextStates[sess.Name]
	s.contextMu.Unlock()
	return state
}

func transcriptNewerThan(path string, capturedAt time.Time) bool {
	if strings.TrimSpace(path) == "" || capturedAt.IsZero() {
		return true
	}
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.ModTime().After(capturedAt)
}

func (s *Server) contextProbeWorkDir(sess *session.Session) string {
	candidates := []string{
		strings.TrimSpace(sess.Metadata.WorkDir),
		strings.TrimSpace(sess.Metadata.WorkspaceRoot),
	}
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates, home)
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	return ""
}

func (s *Server) refreshContextUsage(sess *session.Session) {
	if sess == nil {
		return
	}
	s.contextRefreshSem <- contextRefreshPermit{Acquired: true}
	defer func() { <-s.contextRefreshSem }()

	workSess := *sess
	workSess.Metadata = sess.Metadata
	workSess.Metadata.WorkDir = s.contextProbeWorkDir(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()

	usage, err := sessionctx.NewDefault(&workSess, "", "").Usage(ctx, sessionctx.UsageOptions{})

	s.contextMu.Lock()
	state := s.contextStates[sess.Name]
	state.Refreshing = false
	if err != nil {
		if !state.Loaded {
			state.Status = "failed; retrying"
		}
		state.RetryAfter = time.Now().Add(30 * time.Second)
		s.contextStates[sess.Name] = state
		s.contextMu.Unlock()
		s.log.Warn("daemon.context_usage.refresh.failed",
			"component", "daemon",
			"subcomponent", "context_usage",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"work_dir", workSess.Metadata.WorkDir,
			"err", err)
	} else {
		state.Usage = usage
		state.Loaded = true
		state.Status = ""
		state.RetryAfter = time.Time{}
		s.contextStates[sess.Name] = state
		s.contextMu.Unlock()
		s.log.Info("daemon.context_usage.refresh.completed",
			"component", "daemon",
			"subcomponent", "context_usage",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"source", usage.Source,
			"total_tokens", usage.TotalTokens,
			"max_tokens", usage.MaxTokens)
	}

	store, storeErr := session.NewGlobalFileStore()
	if storeErr != nil {
		s.log.Warn("daemon.context_usage.publish.store_failed",
			"component", "daemon",
			"subcomponent", "context_usage",
			"session", sess.Name,
			"err", storeErr)
		return
	}
	latest, getErr := store.Get(sess.Name)
	if getErr != nil || latest == nil {
		return
	}
	s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_UPDATED, store, latest, "")
}

func (s *Server) renameContextState(oldName, newName string) {
	if oldName == "" || newName == "" || oldName == newName {
		return
	}
	s.contextMu.Lock()
	if state, ok := s.contextStates[oldName]; ok {
		s.contextStates[newName] = state
		delete(s.contextStates, oldName)
	}
	s.contextMu.Unlock()
}

func (s *Server) deleteContextState(name string) {
	if name == "" {
		return
	}
	s.contextMu.Lock()
	delete(s.contextStates, name)
	s.contextMu.Unlock()
}

func (s *Server) sessionSummary(store *session.FileStore, sess *session.Session) *clydev1.SessionSummary {
	settings, _ := store.LoadSettings(sess.Name)
	model := "-"
	if sess.Metadata.TranscriptPath != "" {
		if m := inspectExtractModel(sess.Metadata.TranscriptPath); m != "" {
			model = m
		}
	}
	if model == "-" && settings != nil && settings.Model != "" {
		model = adaptercursor.NormalizeSessionSettingsModel(settings.Model)
	}
	stats := inspectStatsFor(sess.Metadata.TranscriptPath)
	size := int64(0)
	lastActivity := sess.Metadata.LastAccessed
	if p := sess.Metadata.TranscriptPath; p != "" {
		if info, err := os.Stat(p); err == nil {
			size = info.Size()
			if info.ModTime().After(lastActivity) {
				lastActivity = info.ModTime()
			}
		}
	}
	var bridge *clydev1.Bridge
	if sess.Metadata.SessionID != "" {
		s.bridgeMu.RLock()
		if b := s.bridges[sess.Metadata.SessionID]; b != nil {
			cp := proto.Clone(b).(*clydev1.Bridge)
			bridge = cp
		}
		s.bridgeMu.RUnlock()
	}
	contextState := s.contextStateForSession(sess)
	return &clydev1.SessionSummary{
		Name:                  sess.Name,
		MetadataName:          sess.Metadata.Name,
		SessionId:             sess.Metadata.SessionID,
		TranscriptPath:        sess.Metadata.TranscriptPath,
		WorkDir:               sess.Metadata.WorkDir,
		CreatedNanos:          sess.Metadata.Created.UnixNano(),
		LastAccessedNanos:     sess.Metadata.LastAccessed.UnixNano(),
		ParentSession:         sess.Metadata.ParentSession,
		IsForkedSession:       sess.Metadata.IsForkedSession,
		IsIncognito:           sess.Metadata.IsIncognito,
		PreviousSessionIds:    append([]string(nil), sess.Metadata.PreviousSessionIDs...),
		Context:               sess.Metadata.Context,
		HasCustomOutputStyle:  sess.Metadata.HasCustomOutputStyle,
		WorkspaceRoot:         sess.Metadata.WorkspaceRoot,
		ContextMessageCount:   int32(sess.Metadata.ContextMessageCount),
		DisplayTitle:          sess.Metadata.DisplayTitle,
		Model:                 model,
		RemoteControl:         settings != nil && settings.RemoteControl,
		MessageCount:          int32(stats.VisibleMessages),
		TranscriptSizeBytes:   size,
		LastActivityNanos:     lastActivity.UnixNano(),
		Bridge:                bridge,
		ContextTotalTokens:    int32(contextState.Usage.TotalTokens),
		ContextMaxTokens:      int32(contextState.Usage.MaxTokens),
		ContextPercentage:     int32(contextState.Usage.Percentage),
		ContextMessagesTokens: int32(contextState.Usage.CategoryTokens("Messages")),
		ContextUsageLoaded:    contextState.Loaded,
		ContextUsageStatus:    contextState.Status,
	}
}

func (s *Server) sessionDetail(store *session.FileStore, sess *session.Session) *clydev1.GetSessionDetailResponse {
	model := "-"
	if sess.Metadata.TranscriptPath != "" {
		if m := inspectExtractModel(sess.Metadata.TranscriptPath); m != "" {
			model = m
		}
	}
	if model == "-" {
		if settings, _ := store.LoadSettings(sess.Name); settings != nil && settings.Model != "" {
			model = adaptercursor.NormalizeSessionSettingsModel(settings.Model)
		}
	}
	stats := inspectStatsFor(sess.Metadata.TranscriptPath)
	resp := &clydev1.GetSessionDetailResponse{
		SessionName:           sess.Name,
		Model:                 model,
		TotalMessages:         int32(stats.VisibleMessages),
		VisibleTokensEstimate: int32(stats.VisibleTokensEstimate),
		LastMessageTokens:     int32(stats.LastMessageTokens),
		CompactionCount:       int32(stats.CompactionCount),
		LastPreCompactTokens:  int32(stats.LastPreCompactTokens),
	}
	if p := sess.Metadata.TranscriptPath; p != "" {
		if info, err := os.Stat(p); err == nil {
			resp.TranscriptSizeBytes = info.Size()
			resp.LastActivityNanos = info.ModTime().UnixNano()
		}
	}
	for _, m := range inspectRecentMessages(sess.Metadata.TranscriptPath, 5, 150) {
		text := strings.TrimSpace(m.Text)
		if text == "" || strings.HasPrefix(text, "<") || len(text) < 5 {
			continue
		}
		resp.RecentMessages = append(resp.RecentMessages, detailMessageProto(m.Role, text, m.Timestamp))
	}
	for _, m := range inspectAllMessages(sess.Metadata.TranscriptPath, 1000) {
		resp.AllMessages = append(resp.AllMessages, detailMessageProto(m.Role, m.Text, m.Timestamp))
	}
	for _, t := range inspectToolUseStats(sess.Metadata.TranscriptPath, 8) {
		resp.Tools = append(resp.Tools, &clydev1.ToolUse{Name: t.Name, Count: int32(t.Count)})
	}
	return resp
}

func detailMessageProto(role, text string, ts time.Time) *clydev1.DetailMessage {
	out := &clydev1.DetailMessage{Role: role, Text: text}
	if !ts.IsZero() {
		out.TimestampNanos = ts.UnixNano()
	}
	return out
}

// settingsLockFor returns the per-session mutex used to serialise
// settings.json writes. The lock is created lazily so cold sessions
// do not occupy memory.
func (s *Server) settingsLockFor(name string) *sync.Mutex {
	s.settingsLocksMu.Lock()
	defer s.settingsLocksMu.Unlock()
	if m, ok := s.settingsLocks[name]; ok {
		return m
	}
	m := &sync.Mutex{}
	s.settingsLocks[name] = m
	return m
}

// UpdateSessionSettings is the daemon side write path for per session
// settings.json. Mutations from the TUI, CLI, and any other client
// go through here so writes serialise per session and broadcast to
// every subscriber on completion.
func (s *Server) UpdateSessionSettings(ctx context.Context, req *clydev1.UpdateSessionSettingsRequest) (*clydev1.UpdateSessionSettingsResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Get(req.Name)
	if err != nil || sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.Name)
	}
	lock := s.settingsLockFor(req.Name)
	lock.Lock()
	defer lock.Unlock()

	current, _ := store.LoadSettings(req.Name)
	if current == nil {
		current = &session.Settings{}
	}
	applyMask := func(field string) bool {
		if len(req.UpdateMask) == 0 {
			return true
		}
		for _, m := range req.UpdateMask {
			if m == field {
				return true
			}
		}
		return false
	}
	if req.Settings != nil {
		if applyMask("model") {
			current.Model = adaptercursor.NormalizeSessionSettingsModel(req.Settings.Model)
		}
		if applyMask("effort_level") {
			current.EffortLevel = req.Settings.EffortLevel
		}
		if applyMask("output_style") {
			current.OutputStyle = req.Settings.OutputStyle
		}
		if applyMask("remote_control") {
			current.RemoteControl = req.Settings.RemoteControl
		}
	}
	if err := store.SaveSettings(req.Name, current); err != nil {
		return nil, status.Errorf(codes.Internal, "save settings: %v", err)
	}
	s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_UPDATED, store, sess, "")
	s.log.LogAttrs(ctx, slog.LevelInfo, "session settings updated via RPC",
		slog.String("session", req.Name),
		slog.Bool("remote_control", current.RemoteControl),
		slog.String("model", current.Model),
		slog.String("cursor_normalized_model", adaptercursor.NormalizeModelAlias(current.Model)),
		slog.String("effort", current.EffortLevel),
	)
	return &clydev1.UpdateSessionSettingsResponse{}, nil
}

// UpdateGlobalSettings mutates the clyde global config defaults
// from any client. Currently only the remote_control default is
// exposed because that is the field this work needs. The handler
// rewrites the global config TOML through internal/config so callers
// do not need filesystem access.
func (s *Server) UpdateGlobalSettings(ctx context.Context, req *clydev1.UpdateGlobalSettingsRequest) (*clydev1.UpdateGlobalSettingsResponse, error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load global: %v", err)
	}
	applyMask := func(field string) bool {
		if len(req.UpdateMask) == 0 {
			return true
		}
		for _, m := range req.UpdateMask {
			if m == field {
				return true
			}
		}
		return false
	}
	if req.Defaults != nil && applyMask("remote_control") {
		cfg.Defaults.RemoteControl = req.Defaults.RemoteControl
	}
	if err := config.SaveGlobal(cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "save global: %v", err)
	}
	s.publishGlobalSettingsEvent(cfg.Defaults.RemoteControl)
	s.log.LogAttrs(ctx, slog.LevelInfo, "global settings updated via RPC",
		slog.Bool("remote_control", cfg.Defaults.RemoteControl),
	)
	return &clydev1.UpdateGlobalSettingsResponse{}, nil
}

func (s *Server) ListConfigControls(ctx context.Context, _ *clydev1.ListConfigControlsRequest) (*clydev1.ListConfigControlsResponse, error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load global: %v", err)
	}
	descs := config.ListControlDescriptors(cfg)
	out := make([]*clydev1.ConfigControl, 0, len(descs))
	for _, desc := range descs {
		out = append(out, protoConfigControl(desc))
	}
	s.log.LogAttrs(ctx, slog.LevelDebug, "daemon.config_controls.list",
		slog.String("component", "daemon"),
		slog.Int("count", len(out)),
	)
	return &clydev1.ListConfigControlsResponse{Controls: out}, nil
}

func (s *Server) UpdateConfigControl(ctx context.Context, req *clydev1.UpdateConfigControlRequest) (*clydev1.UpdateConfigControlResponse, error) {
	key := strings.TrimSpace(req.GetKey())
	if key == "" {
		return nil, status.Error(codes.InvalidArgument, "key is required")
	}
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load global: %v", err)
	}
	if err := config.UpdateControlValue(cfg, key, req.GetValue()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "update control: %v", err)
	}
	if err := config.SaveGlobal(cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "save global: %v", err)
	}
	if key == "defaults.remote_control" {
		s.publishGlobalSettingsEvent(cfg.Defaults.RemoteControl)
	}
	var updated *clydev1.ConfigControl
	for _, desc := range config.ListControlDescriptors(cfg) {
		if desc.Key == key {
			updated = protoConfigControl(desc)
			break
		}
	}
	s.log.LogAttrs(ctx, slog.LevelInfo, "daemon.config_controls.updated",
		slog.String("component", "daemon"),
		slog.String("key", key),
		slog.String("value", strings.TrimSpace(req.GetValue())),
	)
	return &clydev1.UpdateConfigControlResponse{Control: updated}, nil
}

func protoConfigControl(desc config.ControlDescriptor) *clydev1.ConfigControl {
	options := make([]*clydev1.ConfigControlOption, 0, len(desc.Options))
	for _, opt := range desc.Options {
		options = append(options, &clydev1.ConfigControlOption{
			Value:       opt.Value,
			Label:       opt.Label,
			Description: opt.Description,
		})
	}
	return &clydev1.ConfigControl{
		Key:          desc.Key,
		Section:      desc.Section,
		Label:        desc.Label,
		Description:  desc.Description,
		Type:         protoConfigControlType(desc.Type),
		Value:        desc.Value,
		DefaultValue: desc.DefaultValue,
		Options:      options,
		Sensitive:    desc.Sensitive,
		ReadOnly:     desc.ReadOnly,
	}
}

func protoConfigControlType(kind config.ControlType) clydev1.ConfigControlType {
	switch kind {
	case config.ControlTypeBool:
		return clydev1.ConfigControlType_CONFIG_CONTROL_TYPE_BOOL
	case config.ControlTypeEnum:
		return clydev1.ConfigControlType_CONFIG_CONTROL_TYPE_ENUM
	case config.ControlTypeString:
		return clydev1.ConfigControlType_CONFIG_CONTROL_TYPE_STRING
	case config.ControlTypePath:
		return clydev1.ConfigControlType_CONFIG_CONTROL_TYPE_PATH
	default:
		return clydev1.ConfigControlType_CONFIG_CONTROL_TYPE_UNSPECIFIED
	}
}

// StartRemoteSession creates a canonical clyde session row, persists remote
// control settings, then launches a daemon-owned headless worker that runs
// Claude with --remote-control against the pre-assigned session UUID.
func (s *Server) StartRemoteSession(ctx context.Context, req *clydev1.StartRemoteSessionRequest) (*clydev1.StartRemoteSessionResponse, error) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	basedir := strings.TrimSpace(req.GetBasedir())
	if basedir == "" {
		if basedir, err = os.Getwd(); err != nil {
			return nil, status.Errorf(codes.Internal, "resolve working directory: %v", err)
		}
	}
	if info, err := os.Stat(basedir); err != nil || !info.IsDir() {
		return nil, status.Errorf(codes.InvalidArgument, "basedir %q is not a directory", basedir)
	}

	name := strings.TrimSpace(req.GetSessionName())
	if name == "" {
		name, err = nextRemoteSessionName(store)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "allocate session name: %v", err)
		}
	} else if session.ValidateName(name) != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session name %q", name)
	}
	if store.Exists(name) {
		return nil, status.Errorf(codes.AlreadyExists, "session %q already exists", name)
	}

	sessionID := util.GenerateUUID()
	sess := session.NewSession(name, sessionID)
	sess.Metadata.WorkDir = basedir
	sess.Metadata.WorkspaceRoot = basedir
	sess.Metadata.IsIncognito = req.GetIncognito()
	if err := store.Create(sess); err != nil {
		return nil, status.Errorf(codes.Internal, "create session: %v", err)
	}
	if err := store.SaveSettings(name, &session.Settings{RemoteControl: true}); err != nil {
		_ = store.Delete(name)
		return nil, status.Errorf(codes.Internal, "save session settings: %v", err)
	}
	s.publishSessionSummaryEvent(clydev1.SubscribeRegistryResponse_KIND_SESSION_ADOPTED, store, sess, "")

	cmd, err := s.startRemoteWorkerProcess(name, sessionID, basedir, req.GetIncognito())
	if err != nil {
		_ = store.Delete(name)
		return nil, status.Errorf(codes.Internal, "launch remote session: %v", err)
	}
	worker := &remoteWorker{
		sessionName: name,
		sessionID:   sessionID,
		incognito:   req.GetIncognito(),
		cmd:         cmd,
	}
	s.remoteMu.Lock()
	s.remoteWorkers[name] = worker
	s.remoteMu.Unlock()
	go s.waitRemoteWorker(worker)
	s.log.Info("daemon.remote_session.started",
		"component", "daemon",
		"session", name,
		"session_id", sessionID,
		"basedir", basedir,
		"incognito", req.GetIncognito(),
		"pid", cmd.Process.Pid,
	)
	return &clydev1.StartRemoteSessionResponse{
		SessionName: name,
		SessionId:   sessionID,
		LaunchState: clydev1.StartRemoteSessionResponse_LAUNCH_STATE_LAUNCHING,
	}, nil
}

// ListBridges returns the current set of active claude --remote-control
// bridges as discovered by the bridge watcher.
func (s *Server) ListBridges(_ context.Context, _ *clydev1.ListBridgesRequest) (*clydev1.ListBridgesResponse, error) {
	return &clydev1.ListBridgesResponse{Bridges: s.snapshotBridges()}, nil
}

// snapshotBridges returns a copy of the daemon's current bridge map.
// The webapp uses it to render the active bridge list without going
// through the gRPC surface.
func (s *Server) snapshotBridges() []*clydev1.Bridge {
	s.bridgeMu.RLock()
	defer s.bridgeMu.RUnlock()
	out := make([]*clydev1.Bridge, 0, len(s.bridges))
	for _, b := range s.bridges {
		out = append(out, proto.Clone(b).(*clydev1.Bridge))
	}
	return out
}

// setBridge records a bridge entry and broadcasts BRIDGE_OPENED.
func (s *Server) setBridge(b *clydev1.Bridge) {
	if b == nil || b.SessionId == "" {
		return
	}
	s.bridgeMu.Lock()
	prev, exists := s.bridges[b.SessionId]
	s.bridges[b.SessionId] = b
	s.bridgeMu.Unlock()
	if exists && prev != nil && prev.BridgeSessionId == b.BridgeSessionId {
		return
	}
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:            clydev1.SubscribeRegistryResponse_KIND_BRIDGE_OPENED,
		SessionId:       b.SessionId,
		BridgeSessionId: b.BridgeSessionId,
		BridgeUrl:       b.Url,
	})
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "bridge opened",
		slog.String("session_id", b.SessionId),
		slog.String("bridge", b.BridgeSessionId),
	)
}

// removeBridge clears a bridge entry and broadcasts BRIDGE_CLOSED.
func (s *Server) removeBridge(sessionID string) {
	if sessionID == "" {
		return
	}
	s.bridgeMu.Lock()
	prev, ok := s.bridges[sessionID]
	delete(s.bridges, sessionID)
	s.bridgeMu.Unlock()
	if !ok || prev == nil {
		return
	}
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:            clydev1.SubscribeRegistryResponse_KIND_BRIDGE_CLOSED,
		SessionId:       sessionID,
		BridgeSessionId: prev.BridgeSessionId,
		BridgeUrl:       prev.Url,
	})
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "bridge closed",
		slog.String("session_id", sessionID),
		slog.String("bridge", prev.BridgeSessionId),
	)
}

// resolveTranscriptPath looks up the transcript path for a Claude
// session UUID. Walks the global session store and returns the path
// of the first session whose Metadata.SessionID or PreviousSessionIDs
// matches. Returns the empty string when nothing matches.
func resolveTranscriptPath(sessionID string) string {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return ""
	}
	all, err := store.List()
	if err != nil {
		return ""
	}
	for _, s := range all {
		if s.Metadata.SessionID == sessionID {
			return s.Metadata.TranscriptPath
		}
		for _, prev := range s.Metadata.PreviousSessionIDs {
			if prev == sessionID {
				return s.Metadata.TranscriptPath
			}
		}
	}
	return ""
}

// TailTranscript streams transcript lines for a session via the hub.
// Reference counted so multiple subscribers share one underlying
// fsnotify watcher per transcript.
func (s *Server) TailTranscript(req *clydev1.TailTranscriptRequest, stream clydev1.ClydeService_TailTranscriptServer) error {
	if req.SessionId == "" {
		return status.Error(codes.InvalidArgument, "session_id is required")
	}
	path := resolveTranscriptPath(req.SessionId)
	if path == "" {
		return status.Errorf(codes.NotFound, "no transcript for session %q", req.SessionId)
	}
	startOffset := req.StartAtOffset
	if startOffset == 0 {
		// Default to streaming future lines only. Callers that want
		// the full file pass start_at_offset = 1 (effectively any
		// nonzero positive value before the file size).
		startOffset = -1
	}
	ch, cleanup, err := s.transcripts.Subscribe(req.SessionId, path, startOffset)
	if err != nil {
		return status.Errorf(codes.Internal, "open tailer: %v", err)
	}
	defer cleanup()

	ctx := stream.Context()
	for {
		select {
		case line, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(line); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// SendToSession delivers text into a running claude session via the
// per session inject socket the wrapper opened on launch. Returns
// delivered=false when no socket exists, so callers can fall back to
// telling the user to use the local terminal directly.
func (s *Server) SendToSession(_ context.Context, req *clydev1.SendToSessionRequest) (*clydev1.SendToSessionResponse, error) {
	if req.SessionId == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	socketPath := injectSocketPath(req.SessionId)
	if _, err := os.Stat(socketPath); err != nil {
		return &clydev1.SendToSessionResponse{Delivered: false}, nil
	}
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return &clydev1.SendToSessionResponse{Delivered: false}, nil
	}
	defer conn.Close()
	payload := req.Text
	if !strings.HasSuffix(payload, "\n") {
		payload += "\n"
	}
	n, werr := conn.Write([]byte(payload))
	if werr != nil {
		return &clydev1.SendToSessionResponse{Delivered: false, BytesWritten: int32(n)}, nil
	}
	return &clydev1.SendToSessionResponse{Delivered: true, BytesWritten: int32(n)}, nil
}

func nextRemoteSessionName(store *session.FileStore) (string, error) {
	list, err := store.List()
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(list))
	for _, sess := range list {
		if sess == nil {
			continue
		}
		taken[sess.Name] = true
	}
	base := "chat-" + time.Now().UTC().Format("20060102-150405")
	name := session.UniqueName(base, taken)
	if name == "" {
		return "", fmt.Errorf("could not allocate a unique session name")
	}
	return name, nil
}

func (s *Server) startRemoteWorkerProcess(sessionName, sessionID, basedir string, incognito bool) (*exec.Cmd, error) {
	self, err := remoteWorkerExecutable()
	if err != nil {
		return nil, err
	}
	args := []string{
		"daemon",
		"launch-remote-worker",
		"--session-name", sessionName,
		"--session-id", sessionID,
		"--basedir", basedir,
	}
	if incognito {
		args = append(args, "--incognito")
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, args...)
	cmd.Dir = basedir
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	if err := cmd.Start(); err != nil {
		_ = devNull.Close()
		return nil, err
	}
	_ = devNull.Close()
	return cmd, nil
}

func (s *Server) waitRemoteWorker(worker *remoteWorker) {
	if worker == nil || worker.cmd == nil {
		return
	}
	err := worker.cmd.Wait()
	s.remoteMu.Lock()
	delete(s.remoteWorkers, worker.sessionName)
	s.remoteMu.Unlock()
	level := slog.LevelInfo
	if err != nil {
		level = slog.LevelWarn
	}
	s.log.LogAttrs(context.Background(), level, "daemon.remote_session.exited",
		slog.String("component", "daemon"),
		slog.String("session", worker.sessionName),
		slog.String("session_id", worker.sessionID),
		slog.Bool("incognito", worker.incognito),
		slog.Any("err", err),
	)
	if !worker.incognito {
		return
	}
	if _, delErr := s.DeleteSession(context.Background(), &clydev1.DeleteSessionRequest{Name: worker.sessionName}); delErr != nil {
		s.log.Warn("daemon.remote_session.incognito_cleanup_failed",
			"component", "daemon",
			"session", worker.sessionName,
			"session_id", worker.sessionID,
			"err", delErr,
		)
	}
}

func (s *Server) ProbeContextUsage(ctx context.Context, req *clydev1.ProbeContextUsageRequest) (*clydev1.ProbeContextUsageResponse, error) {
	if req.GetSessionName() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_name is required")
	}
	started := time.Now()
	s.log.Info("daemon.context_usage.probe.started",
		"component", "daemon",
		"subcomponent", "context_usage",
		"session", req.GetSessionName())
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Resolve(req.GetSessionName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve session: %v", err)
	}
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionName())
	}
	usage, err := compactengine.ProbeContextUsage(ctx, compactengine.ProbeOptions{
		SessionID:   sess.Metadata.SessionID,
		WorkDir:     s.contextProbeWorkDir(sess),
		Timeout:     60 * time.Second,
		ForkSession: true,
	})
	if err != nil {
		s.log.Warn("daemon.context_usage.probe.failed",
			"component", "daemon",
			"subcomponent", "context_usage",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"duration_ms", time.Since(started).Milliseconds(),
			"err", err)
		return nil, status.Errorf(codes.Internal, "probe context usage: %v", err)
	}
	resp := &clydev1.ProbeContextUsageResponse{
		SessionName: sess.Name,
		SessionId:   sess.Metadata.SessionID,
		Model:       usage.Model,
		TotalTokens: int32(usage.TotalTokens),
		MaxTokens:   int32(usage.MaxTokens),
		Percentage:  int32(usage.Percentage),
		Categories:  make([]*clydev1.ContextUsageCategory, 0, len(usage.Categories)),
	}
	for _, cat := range usage.Categories {
		resp.Categories = append(resp.Categories, &clydev1.ContextUsageCategory{
			Name:       cat.Name,
			Tokens:     int32(cat.Tokens),
			Color:      cat.Color,
			IsDeferred: cat.IsDeferred,
		})
	}
	s.log.Info("daemon.context_usage.probe.completed",
		"component", "daemon",
		"subcomponent", "context_usage",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"duration_ms", time.Since(started).Milliseconds(),
		"total_tokens", usage.TotalTokens,
		"max_tokens", usage.MaxTokens,
		"percentage", usage.Percentage)
	return resp, nil
}

func (s *Server) CompactPreview(
	req *clydev1.CompactRunRequest,
	stream clydev1.ClydeService_CompactPreviewServer,
) error {
	s.log.Info("daemon.compact.preview.started",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
		"target", req.GetTargetTokens(),
	)
	return s.runCompact(stream.Context(), req, stream, compactengine.RuntimeModePreview)
}

func (s *Server) CompactApply(
	req *clydev1.CompactRunRequest,
	stream clydev1.ClydeService_CompactApplyServer,
) error {
	s.log.Info("daemon.compact.apply.started",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
		"target", req.GetTargetTokens(),
	)
	return s.runCompact(stream.Context(), req, stream, compactengine.RuntimeModeApply)
}

func (s *Server) runCompact(
	ctx context.Context,
	req *clydev1.CompactRunRequest,
	stream compactEventStream,
	mode compactengine.RuntimeMode,
) error {
	if req.GetSessionName() == "" {
		return status.Error(codes.InvalidArgument, "session_name is required")
	}
	s.log.Debug("daemon.compact.run.begin",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
		"target", req.GetTargetTokens(),
		"mode", mode,
	)
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Resolve(req.GetSessionName())
	if err != nil {
		return status.Errorf(codes.Internal, "resolve session: %v", err)
	}
	if sess == nil {
		return status.Errorf(codes.NotFound, "session %q not found", req.GetSessionName())
	}
	if mode == compactengine.RuntimeModeApply && !req.GetForce() && s.sessionIsActive(sess.Name) {
		return status.Errorf(codes.FailedPrecondition, "session %q is currently open; exit it first or pass --force", sess.Name)
	}

	strippers := compactengine.Strippers{}
	if req.Strippers != nil {
		strippers = compactengine.Strippers{
			Thinking: req.Strippers.Thinking,
			Images:   req.Strippers.Images,
			Tools:    req.Strippers.Tools,
			Chat:     req.Strippers.Chat,
		}
	}
	if !strippers.Any() && req.GetTargetTokens() > 0 {
		strippers.SetAll()
	}

	var sequence int32
	send := func(ev *clydev1.CompactEvent) error {
		sequence++
		ev.Sequence = sequence
		return stream.Send(ev)
	}
	if err := send(&clydev1.CompactEvent{
		Kind:    clydev1.CompactEvent_KIND_STATUS,
		Message: "loading transcript and probing context...",
	}); err != nil {
		return err
	}

	modelForCount := req.GetModel()
	modelForRender := req.GetModel()
	if !req.GetModelExplicit() {
		modelForCount, modelForRender, _ = compactengine.ResolveModelForCounting(store, sess, req.GetModel())
	}
	upfront, staticOverhead, slice, upfrontErr := compactengine.BuildRuntimeUpfront(ctx, compactengine.RuntimeRequest{
		Session:      sess,
		Store:        store,
		TargetTokens: int(req.GetTargetTokens()),
		Reserved:     int(req.GetReservedTokens()),
		Model:        modelForCount,
		Strippers:    strippers,
	}, modelForRender)
	if upfrontErr != nil {
		return status.Errorf(codes.Internal, "build compact upfront: %v", upfrontErr)
	}
	if err := send(&clydev1.CompactEvent{
		Kind:    clydev1.CompactEvent_KIND_STATUS,
		Message: "planning compaction...",
	}); err != nil {
		return err
	}
	if err := send(&clydev1.CompactEvent{
		Kind: clydev1.CompactEvent_KIND_UPFRONT,
		Upfront: &clydev1.CompactUpfront{
			SessionName:     upfront.SessionName,
			SessionId:       upfront.SessionID,
			Model:           upfront.Model,
			CurrentTotal:    int32(upfront.CurrentTotal),
			MaxTokens:       int32(upfront.MaxTokens),
			TargetTokens:    int32(upfront.Target),
			StaticFloor:     int32(upfront.StaticFloor),
			ReservedTokens:  int32(upfront.Reserved),
			ThinkingBlocks:  int32(upfront.Thinking),
			ImageBlocks:     int32(upfront.Images),
			ToolPairs:       int32(upfront.ToolPairs),
			ChatTurns:       int32(upfront.ChatTurns),
			StrippersText:   upfront.StrippersText,
			CalibrationDate: upfront.TargetDate,
		},
	}); err != nil {
		return err
	}

	onIteration := func(iter compactengine.RuntimeIteration) {
		_ = send(&clydev1.CompactEvent{
			Kind: clydev1.CompactEvent_KIND_ITERATION,
			Iteration: &clydev1.CompactIteration{
				Iteration:         sequence,
				Step:              iter.Iteration.Step,
				TailTokens:        int32(iter.Iteration.TailTokens),
				CtxTotal:          int32(iter.Iteration.CtxTotal),
				Delta:             int32(iter.Iteration.Delta),
				ThinkingDropped:   iter.Iteration.ThinkingDropped,
				ImagesPlaceholder: iter.Iteration.ImagesPlaceholder,
				ToolsFull:         int32(iter.Iteration.ToolsFull),
				ToolsLineOnly:     int32(iter.Iteration.ToolsLineOnly),
				ToolsDropped:      int32(iter.Iteration.ToolsDropped),
				ChatTurnsTotal:    int32(iter.Iteration.ChatTurnsTotal),
				ChatTurnsDropped:  int32(iter.Iteration.ChatTurnsDropped),
			},
		})
	}

	result, runErr := compactengine.RunRuntime(ctx, compactengine.RuntimeRequest{
		Session:                sess,
		Store:                  store,
		TargetTokens:           int(req.GetTargetTokens()),
		Reserved:               int(req.GetReservedTokens()),
		Model:                  modelForCount,
		ModelExplicit:          req.GetModelExplicit(),
		Strippers:              strippers,
		Summarize:              req.GetSummarize(),
		Force:                  req.GetForce(),
		Mode:                   mode,
		PreparedUpfront:        &upfront,
		PreparedStaticOverhead: staticOverhead,
		PreparedSlice:          slice,
	}, onIteration)
	if runErr != nil {
		s.log.Error("daemon.compact.run_failed",
			"component", "daemon",
			"subcomponent", "compact",
			"session", req.GetSessionName(),
			"err", runErr.Error(),
		)
		return status.Errorf(codes.Internal, "compact runtime: %v", runErr)
	}

	final := &clydev1.CompactFinal{
		BaselineTail:   int32(result.Plan.BaselineTail),
		FinalTail:      int32(result.Plan.FinalTail),
		HitTarget:      result.Plan.HitTarget,
		TargetTokens:   int32(result.Upfront.Target),
		StaticFloor:    int32(result.Upfront.StaticFloor),
		ReservedTokens: int32(result.Upfront.Reserved),
		TranscriptPath: result.TranscriptPath,
	}
	if err := send(&clydev1.CompactEvent{
		Kind:  clydev1.CompactEvent_KIND_FINAL,
		Final: final,
	}); err != nil {
		return err
	}

	if result.Apply != nil {
		if err := send(&clydev1.CompactEvent{
			Kind: clydev1.CompactEvent_KIND_APPLY_MUTATION,
			ApplyMutation: &clydev1.CompactApplyMutation{
				BoundaryUuid:    result.Apply.BoundaryUUID,
				SyntheticUuid:   result.Apply.SyntheticUUID,
				PreApplyOffset:  result.Apply.PreApplyOffset,
				PostApplyOffset: result.Apply.PostApplyOffset,
				SnapshotPath:    result.Apply.SnapshotPath,
				LedgerPath:      result.Apply.LedgerPath,
			},
		}); err != nil {
			return err
		}
	}
	s.log.Info("daemon.compact.run_completed",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
		"session_id", sess.Metadata.SessionID,
		"mode", mode,
		"hit_target", result.Plan.HitTarget,
		"final_tail", result.Plan.FinalTail,
	)
	return nil
}

func (s *Server) CompactUndo(ctx context.Context, req *clydev1.CompactUndoRequest) (*clydev1.CompactUndoResponse, error) {
	if req.GetSessionName() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_name is required")
	}
	s.log.Info("daemon.compact.undo.started",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
	)
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	sess, err := store.Resolve(req.GetSessionName())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve session: %v", err)
	}
	if sess == nil {
		return nil, status.Errorf(codes.NotFound, "session %q not found", req.GetSessionName())
	}
	path := sess.Metadata.TranscriptPath
	if path == "" {
		return nil, status.Error(codes.InvalidArgument, "session has no transcript")
	}
	entry, undoErr := compactengine.Undo(sess.Metadata.SessionID, path)
	if undoErr != nil {
		s.log.Error("daemon.compact.undo_failed",
			"component", "daemon",
			"subcomponent", "compact",
			"session", req.GetSessionName(),
			"session_id", sess.Metadata.SessionID,
			"err", undoErr.Error(),
		)
		return nil, status.Errorf(codes.Internal, "undo: %v", undoErr)
	}
	ledgerPath, _ := compactengine.LedgerPath(sess.Metadata.SessionID)
	postBytes := int64(-1)
	if stat, statErr := os.Stat(path); statErr == nil {
		postBytes = stat.Size()
	}
	resp := &clydev1.CompactUndoResponse{
		SessionName:    sess.Name,
		SessionId:      sess.Metadata.SessionID,
		TranscriptPath: path,
		LedgerPath:     ledgerPath,
		AppliedAt:      entry.Timestamp.UTC().Format(time.RFC3339),
		TargetTokens:   int32(entry.Target),
		BoundaryUuid:   entry.BoundaryUUID,
		SyntheticUuid:  entry.SyntheticUUID,
		SnapshotPath:   entry.SnapshotPath,
		PreApplyOffset: entry.PreApplyOffset,
		PostUndoBytes:  postBytes,
	}
	s.log.Info("daemon.compact.undo_completed",
		"component", "daemon",
		"subcomponent", "compact",
		"session", req.GetSessionName(),
		"session_id", sess.Metadata.SessionID,
		"pre_apply_offset", entry.PreApplyOffset,
		"post_undo_bytes", postBytes,
	)
	return resp, nil
}

type compactEventStream interface {
	Send(*clydev1.CompactEvent) error
}

// injectSocketPath returns the per session Unix socket path the
// wrapper opens when launched with --remote-control. The directory is
// created lazily by the wrapper.
func injectSocketPath(sessionID string) string {
	dir := injectSocketDir()
	return filepath.Join(dir, sessionID+".sock")
}

// injectSocketDir is the per user directory that holds inject sockets.
// Lives under XDG_RUNTIME_DIR when set, otherwise the per user temp
// directory so macOS users (no XDG_RUNTIME_DIR by default) still get a
// session local path.
func injectSocketDir() string {
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		return filepath.Join(xdg, "clyde", "inject")
	}
	return filepath.Join(os.TempDir(), "clyde-inject")
}
