// Package daemon implements the clyde daemon gRPC server.
// It manages per-session settings.json files so that /model changes
// in one Claude session don't leak to others. The daemon is lazily
// started on first use and exits after an idle timeout.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/bridge"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/session"
)

// Server implements the Clyde gRPC service.
type Server struct {
	clydev1.UnimplementedClydeServiceServer

	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher        *fsnotify.Watcher
	bridgeWatcher  *bridge.Watcher
	globalSettings map[string]any // last-known global settings.json content

	// scanWake fires the discovery scanner immediately. Buffered so a
	// trigger never blocks the caller.
	scanWake chan struct{}

	// subscribers receive registry events as they happen. The mutex
	// guards the map so subscribe and broadcast can run concurrently.
	subMu       sync.Mutex
	subscribers map[chan *clydev1.SubscribeRegistryResponse]struct{}

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
	transcripts *transcriptHub
}

// wrapperSession holds runtime state for one active claude wrapper process.
type wrapperSession struct {
	wrapperID   string
	sessionName string // empty for bare claude invocations
	model       string
	effortLevel string
}

// New creates a new daemon Server and starts watching the global settings file.
func New(log *slog.Logger) (*Server, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create settings watcher: %w", err)
	}

	s := &Server{
		log:           log,
		sessions:      make(map[string]*wrapperSession),
		watcher:       watcher,
		bridgeWatcher: nil,
		scanWake:      make(chan struct{}, 4),
		subscribers:   make(map[chan *clydev1.SubscribeRegistryResponse]struct{}),
		settingsLocks: make(map[string]*sync.Mutex),
		bridges:       make(map[string]*clydev1.Bridge),
		transcripts:   newTranscriptHub(),
	}

	globalPath := globalSettingsPath()
	if err := s.loadGlobalSettings(); err != nil {
		log.LogAttrs(context.Background(), slog.LevelWarn, "global settings load failed on startup",
			slog.String("path", globalPath),
			slog.Any("err", err),
		)
	} else {
		globalModel, _ := s.globalSettings["model"].(string)
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
			s.publishEvent(&clydev1.SubscribeRegistryResponse{
				Kind:        clydev1.SubscribeRegistryResponse_KIND_SESSION_ADOPTED,
				SessionName: a.Name,
				SessionId:   a.Metadata.SessionID,
			})
		}
		s.log.LogAttrs(context.Background(), slog.LevelInfo, "discovery adopted sessions",
			slog.Int("count", len(adopted)),
			slog.Any("names", names),
		)
	} else {
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "discovery scan: nothing new",
			slog.Int("transcripts", len(results)),
		)
	}
}

// TriggerScan implements the RPC. The daemon's scanner runs whenever
// the request lands; the response carries any sessions adopted by the
// previous scan tick so the caller has immediate confirmation.
// Subscribers also receive a SESSION_ADOPTED event for each new entry.
func (s *Server) TriggerScan(ctx context.Context, _ *clydev1.TriggerScanRequest) (*clydev1.TriggerScanResponse, error) {
	select {
	case s.scanWake <- struct{}{}:
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
	s.subscribers[ch] = struct{}{}
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
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:        clydev1.SubscribeRegistryResponse_KIND_SESSION_RENAMED,
		SessionName: req.NewName,
		OldName:     req.OldName,
	})
	s.log.LogAttrs(ctx, slog.LevelInfo, "session renamed via RPC",
		slog.String("old", req.OldName),
		slog.String("new", req.NewName),
	)
	return &clydev1.RenameSessionResponse{}, nil
}

// DeleteSession is the daemon-side delete. It removes the session
// metadata from the registry and broadcasts SESSION_DELETED so all
// connected dashboards prune the row immediately. Transcript and
// agent log cleanup live in the cmd layer because they reach into
// per-project state outside the daemon's scope.
func (s *Server) DeleteSession(ctx context.Context, req *clydev1.DeleteSessionRequest) (*clydev1.DeleteSessionResponse, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store init: %v", err)
	}
	if err := store.Delete(req.Name); err != nil {
		return nil, status.Errorf(codes.Internal, "delete failed: %v", err)
	}
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:        clydev1.SubscribeRegistryResponse_KIND_SESSION_DELETED,
		SessionName: req.Name,
	})
	s.log.LogAttrs(ctx, slog.LevelInfo, "session deleted via RPC",
		slog.String("name", req.Name),
	)
	return &clydev1.DeleteSessionResponse{}, nil
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

// Close shuts down the watcher and cleans up all active session runtime dirs.
func (s *Server) Close() {
	bridge.Close(s.bridgeWatcher)
	if s.watcher != nil {
		_ = s.watcher.Close()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
	}
	s.log.LogAttrs(context.Background(), slog.LevelInfo, "daemon closed",
		slog.Int("cleaned_sessions", len(s.sessions)),
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
		s.log.LogAttrs(ctx, slog.LevelDebug, "daemon.hook.update_context.stubbed",
			slog.String("component", "daemon"),
			slog.String("subject", "hook"),
			slog.String("session", payload.SessionName),
		)
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
	globalCopy := make(map[string]any, len(s.globalSettings))
	for k, v := range s.globalSettings {
		globalCopy[k] = v
	}
	globalModel, _ := s.globalSettings["model"].(string)
	s.mu.RUnlock()

	if model != "" {
		globalCopy["model"] = model
	}
	if effortLevel != "" {
		globalCopy["effortLevel"] = effortLevel
	}

	effectiveModel, _ := globalCopy["model"].(string)
	s.log.LogAttrs(context.Background(), slog.LevelDebug, "writing per-session settings",
		slog.String("wrapper_id", wrapperID),
		slog.String("global_model", globalModel),
		slog.String("session_model", model),
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
				if err := s.loadGlobalSettings(); err != nil {
					s.log.LogAttrs(context.Background(), slog.LevelWarn, "failed to reload global settings",
						slog.Any("err", err),
					)
					continue
				}
				s.syncAllSessions()
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
			s.globalSettings = make(map[string]any)
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("failed to read global settings: %w", err)
	}

	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		return fmt.Errorf("failed to parse global settings: %w", err)
	}

	model, _ := settings["model"].(string)
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
	globalModel, _ := s.globalSettings["model"].(string)
	globalEffort, _ := s.globalSettings["effortLevel"].(string)
	s.mu.RUnlock()

	model = globalModel
	effortLevel = globalEffort

	if sessionName == "" {
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "no session name, using global settings",
			slog.String("model", model),
			slog.String("effort", effortLevel),
		)
		return model, effortLevel
	}

	// Load session-specific settings from clyde's global store
	sessSettings := loadClydeSessionSettings(sessionName)
	if sessSettings != nil {
		if sessSettings.Model != "" {
			model = sessSettings.Model
		}
		if sessSettings.EffortLevel != "" {
			effortLevel = sessSettings.EffortLevel
		}
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "resolved session settings",
			slog.String("session", sessionName),
			slog.String("model", model),
			slog.String("effort", effortLevel),
		)
	} else {
		s.log.LogAttrs(context.Background(), slog.LevelDebug, "no clyde session settings, using global",
			slog.String("session", sessionName),
			slog.String("model", model),
			slog.String("effort", effortLevel),
		)
	}

	return model, effortLevel
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
			current.Model = req.Settings.Model
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
	s.publishEvent(&clydev1.SubscribeRegistryResponse{
		Kind:        clydev1.SubscribeRegistryResponse_KIND_SESSION_UPDATED,
		SessionName: req.Name,
		SessionId:   sess.Metadata.SessionID,
	})
	s.log.LogAttrs(ctx, slog.LevelInfo, "session settings updated via RPC",
		slog.String("session", req.Name),
		slog.Bool("remote_control", current.RemoteControl),
		slog.String("model", current.Model),
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
	s.log.LogAttrs(ctx, slog.LevelInfo, "global settings updated via RPC",
		slog.Bool("remote_control", cfg.Defaults.RemoteControl),
	)
	return &clydev1.UpdateGlobalSettingsResponse{}, nil
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
