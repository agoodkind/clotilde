// Package daemon implements the clotilde daemon gRPC server.
// It manages per-session settings.json files so that /model changes
// in one Claude session don't leak to others. The daemon is lazily
// started on first use and exits after an idle timeout.
package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fgrehm/clotilde/api/daemonpb"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
)

const idleTimeout = 5 * time.Minute

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher        *fsnotify.Watcher
	globalSettings map[string]any // last-known global settings.json content

	// idleTimer fires when the daemon has had zero sessions for idleTimeout.
	// Reset on every AcquireSession; stopped while sessions are active.
	idleTimer *time.Timer
	shutdown  func() // called when idle timer fires; set by Run()
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
		log:       log,
		sessions:  make(map[string]*wrapperSession),
		watcher:   watcher,
		idleTimer: time.NewTimer(idleTimeout),
	}

	globalPath := globalSettingsPath()
	if err := s.loadGlobalSettings(); err != nil {
		log.Warn("global settings load failed on startup", "path", globalPath, "err", err)
	} else {
		globalModel, _ := s.globalSettings["model"].(string)
		log.Info("global settings loaded", "path", globalPath, "model", globalModel, "keys", len(s.globalSettings))
	}

	if err := watcher.Add(globalPath); err != nil {
		log.Warn("failed to watch global settings", "path", globalPath, "err", err)
	} else {
		log.Info("watching global settings", "path", globalPath)
	}

	go s.watchGlobalSettings()
	go s.watchIdleTimeout()

	return s, nil
}

// SetShutdown sets the function called when the idle timer fires.
func (s *Server) SetShutdown(fn func()) {
	s.mu.Lock()
	s.shutdown = fn
	s.mu.Unlock()
}

// watchIdleTimeout exits the daemon when the idle timer fires.
func (s *Server) watchIdleTimeout() {
	<-s.idleTimer.C

	s.mu.RLock()
	n := len(s.sessions)
	fn := s.shutdown
	s.mu.RUnlock()

	if n > 0 {
		// Sessions appeared between timer fire and check. Reset.
		s.idleTimer.Reset(idleTimeout)
		go s.watchIdleTimeout()
		return
	}

	s.log.Info("idle timeout reached, shutting down", "timeout", idleTimeout)
	if fn != nil {
		fn()
	}
}

// Close shuts down the watcher and cleans up all active session runtime dirs.
func (s *Server) Close() {
	_ = s.watcher.Close()

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, sess := range s.sessions {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
	}
	s.log.Info("daemon closed", "cleaned_sessions", len(s.sessions))
}

// AcquireSession writes a per-session settings.json (global settings with
// model overridden) and returns the path along with the real claude binary.
func (s *Server) AcquireSession(ctx context.Context, req *daemonpb.AcquireSessionRequest) (*daemonpb.AcquireSessionResponse, error) {
	if req.WrapperId == "" || req.WrapperId == "__probe__" {
		return nil, status.Error(codes.InvalidArgument, "wrapper_id is required")
	}

	// Check if this session already has a settings file on disk (re-acquire
	// after daemon restart). Preserve its current model/effort rather than
	// resetting to global defaults.
	existingModel, existingEffort := s.readSessionSettings(req.WrapperId)

	var model, effortLevel string
	if existingModel != "" || existingEffort != "" {
		// Re-registering after daemon restart — keep what claude has.
		model = existingModel
		effortLevel = existingEffort
		s.log.Info("re-acquired session with preserved settings",
			"wrapper_id", req.WrapperId,
			"model", model,
			"effort", effortLevel,
		)
	} else {
		// Fresh session — resolve from clotilde session settings + global.
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
	s.idleTimer.Stop()
	s.mu.Unlock()

	realClaude, err := findRealClaude()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to find real claude binary: %v", err)
	}

	s.log.Info("session acquired",
		"wrapper_id", req.WrapperId,
		"session", req.SessionName,
		"model", model,
		"settings_file", settingsFile,
		"claude_bin", realClaude,
		"active_sessions", len(s.sessions),
	)

	return &daemonpb.AcquireSessionResponse{
		RealClaude:   realClaude,
		Model:        model,
		SettingsFile: settingsFile,
	}, nil
}

// ReleaseSession removes the per-session runtime dir after claude exits.
// When the last session is released, the idle timer starts.
func (s *Server) ReleaseSession(ctx context.Context, req *daemonpb.ReleaseSessionRequest) (*daemonpb.ReleaseSessionResponse, error) {
	s.mu.Lock()
	sess, ok := s.sessions[req.WrapperId]
	if ok {
		delete(s.sessions, req.WrapperId)
	}
	remaining := len(s.sessions)
	if remaining == 0 {
		s.idleTimer.Reset(idleTimeout)
	}
	s.mu.Unlock()

	if ok {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
		s.log.Info("session released",
			"wrapper_id", req.WrapperId,
			"session", sess.sessionName,
			"model", sess.model,
			"active_sessions", remaining,
		)
		if remaining == 0 {
			s.log.Info("no active sessions, idle timer started", "timeout", idleTimeout)
		}
	} else {
		s.log.Warn("release for unknown session", "wrapper_id", req.WrapperId)
	}

	return &daemonpb.ReleaseSessionResponse{}, nil
}

// hookEventPayload is the JSON structure sent via HookEvent RPC.
type hookEventPayload struct {
	Type          string   `json:"type"`
	SessionName   string   `json:"session_name,omitempty"`
	WorkspaceRoot string   `json:"workspace_root,omitempty"`
	Messages      []string `json:"messages,omitempty"` // pre-extracted recent messages
}

// HookEvent processes a Claude Code hook event forwarded from a wrapper process.
func (s *Server) HookEvent(ctx context.Context, req *daemonpb.HookEventRequest) (*daemonpb.HookEventResponse, error) {
	var payload hookEventPayload
	if err := json.Unmarshal(req.RawJson, &payload); err != nil {
		return &daemonpb.HookEventResponse{ExitCode: 0}, nil
	}

	switch payload.Type {
	case "update_context":
		// Fire and forget — run in background goroutine
		go s.updateSessionContext(payload)
	}

	return &daemonpb.HookEventResponse{ExitCode: 0}, nil
}

// updateSessionContext generates a one-sentence context summary via sonnet
// and stores it in the session metadata. Runs in a background goroutine.
func (s *Server) updateSessionContext(payload hookEventPayload) {
	if payload.SessionName == "" || len(payload.Messages) == 0 {
		return
	}

	s.log.Info("updating session context", "session", payload.SessionName)

	// Build prompt
	var promptParts []string
	promptParts = append(promptParts, "Based on these recent messages from a coding session, write ONE short sentence (under 15 words) describing what this session is currently working on. Be specific — mention the project, feature, or task name. Output ONLY the sentence, nothing else.")
	promptParts = append(promptParts, "")

	// Include project memory if available
	if payload.WorkspaceRoot != "" {
		memPath := projectMemoryPathFromRoot(payload.WorkspaceRoot)
		if memPath != "" {
			if content, err := os.ReadFile(memPath); err == nil && len(content) > 0 {
				mem := string(content)
				if len(mem) > 2000 {
					mem = mem[:2000]
				}
				promptParts = append(promptParts, "Project memory index:")
				promptParts = append(promptParts, mem)
				promptParts = append(promptParts, "")
			}
		}
	}

	promptParts = append(promptParts, "Recent messages:")
	promptParts = append(promptParts, payload.Messages...)

	prompt := strings.Join(promptParts, "\n")

	// Call claude -p --model sonnet
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	claudeBin, err := findRealClaude()
	if err != nil {
		s.log.Warn("context update: claude not found", "err", err)
		return
	}

	cmd := exec.CommandContext(ctx, claudeBin, "-p", "--model", "sonnet", prompt)
	output, err := cmd.Output()
	if err != nil {
		s.log.Warn("context update: claude -p failed", "session", payload.SessionName, "err", err)
		return
	}

	summary := strings.TrimSpace(string(output))
	if summary == "" || len(summary) > 200 {
		s.log.Warn("context update: bad summary", "session", payload.SessionName, "len", len(summary))
		return
	}

	// Update session metadata in global store
	store, err := session.NewGlobalFileStore()
	if err != nil {
		s.log.Warn("context update: store error", "err", err)
		return
	}
	sess, err := store.Get(payload.SessionName)
	if err != nil {
		s.log.Warn("context update: session not found", "session", payload.SessionName, "err", err)
		return
	}
	sess.Metadata.Context = summary
	if err := store.Update(sess); err != nil {
		s.log.Warn("context update: update failed", "session", payload.SessionName, "err", err)
		return
	}

	s.log.Info("context updated", "session", payload.SessionName, "context", summary)
}

// projectMemoryPathFromRoot returns the path to MEMORY.md for a workspace root.
func projectMemoryPathFromRoot(workspaceRoot string) string {
	if workspaceRoot == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Encode workspace path the same way Claude does: replace / and . with -
	encoded := strings.ReplaceAll(workspaceRoot, "/", "-")
	encoded = strings.ReplaceAll(encoded, ".", "-")
	memPath := filepath.Join(home, ".claude", "projects", encoded, "memory", "MEMORY.md")
	if _, statErr := os.Stat(memPath); statErr == nil {
		return memPath
	}
	return ""
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
	s.log.Debug("writing per-session settings",
		"wrapper_id", wrapperID,
		"global_model", globalModel,
		"session_model", model,
		"session_effort", effortLevel,
		"effective_model", effectiveModel,
		"settings_keys", len(globalCopy),
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
		s.log.Debug("syncing session",
			"wrapper_id", sess.wrapperID,
			"session", sess.sessionName,
			"preserved_model", sess.model,
			"preserved_effort", sess.effortLevel,
		)
		if _, err := s.writeSettingsJSON(sess.wrapperID, sess.model, sess.effortLevel); err != nil {
			s.log.Warn("failed to sync settings", "wrapper_id", sess.wrapperID, "err", err)
		}
	}

	s.log.Info("global settings synced to all sessions", "active_sessions", len(sessions))
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
				s.log.Debug("global settings file changed", "event", event.Op.String())
				if err := s.loadGlobalSettings(); err != nil {
					s.log.Warn("failed to reload global settings", "err", err)
					continue
				}
				s.syncAllSessions()
			}

		case err, ok := <-s.watcher.Errors:
			if !ok {
				return
			}
			s.log.Warn("settings watcher error", "err", err)
		}
	}
}

// loadGlobalSettings reads ~/.claude/settings.json into memory.
func (s *Server) loadGlobalSettings() error {
	path := globalSettingsPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.log.Debug("global settings file not found, using empty", "path", path)
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
	s.log.Debug("global settings reloaded", "model", model, "keys", len(settings))

	s.mu.Lock()
	s.globalSettings = settings
	s.mu.Unlock()

	return nil
}

// resolveSessionSettings loads per-session model and effortLevel from the
// clotilde session's settings.json, falling back to global settings for any
// field not set at the session level.
func (s *Server) resolveSessionSettings(sessionName string) (model, effortLevel string) {
	s.mu.RLock()
	globalModel, _ := s.globalSettings["model"].(string)
	globalEffort, _ := s.globalSettings["effortLevel"].(string)
	s.mu.RUnlock()

	model = globalModel
	effortLevel = globalEffort

	if sessionName == "" {
		s.log.Debug("no session name, using global settings", "model", model, "effort", effortLevel)
		return model, effortLevel
	}

	// Load session-specific settings from clotilde's global store
	sessSettings := loadClotildeSessionSettings(sessionName)
	if sessSettings != nil {
		if sessSettings.Model != "" {
			model = sessSettings.Model
		}
		if sessSettings.EffortLevel != "" {
			effortLevel = sessSettings.EffortLevel
		}
		s.log.Debug("resolved session settings", "session", sessionName, "model", model, "effort", effortLevel)
	} else {
		s.log.Debug("no clotilde session settings, using global", "session", sessionName, "model", model, "effort", effortLevel)
	}

	return model, effortLevel
}

// loadClotildeSessionSettings loads settings.json from the clotilde global
// store for the given session name. Returns nil if not found.
func loadClotildeSessionSettings(sessionName string) *session.Settings {
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
