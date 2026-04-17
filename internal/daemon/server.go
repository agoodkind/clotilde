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
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/fgrehm/clotilde/api/daemonpb"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
)

// Server implements the AgentGateD gRPC service.
type Server struct {
	daemonpb.UnimplementedAgentGateDServer

	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[string]*wrapperSession // keyed by wrapper_id

	watcher        *fsnotify.Watcher
	globalSettings map[string]any // last-known global settings.json content

	// scanWake fires the discovery scanner immediately. Buffered so a
	// trigger never blocks the caller.
	scanWake chan struct{}

	// subscribers receive registry events as they happen. The mutex
	// guards the map so subscribe and broadcast can run concurrently.
	subMu       sync.Mutex
	subscribers map[chan *daemonpb.RegistryEvent]struct{}
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
		log:         log,
		sessions:    make(map[string]*wrapperSession),
		watcher:     watcher,
		scanWake:    make(chan struct{}, 4),
		subscribers: make(map[chan *daemonpb.RegistryEvent]struct{}),
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
	go s.runDiscoveryScanner()

	return s, nil
}

// runDiscoveryScanner periodically walks ~/.claude/projects and adopts
// any transcripts whose UUID is not already tracked by clotilde. Runs
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
			s.log.Debug("discovery scan wake from SIGUSR1")
		case <-s.scanWake:
			s.log.Debug("discovery scan wake from TriggerScan RPC")
		}
	}
}

func (s *Server) runDiscoveryOnce() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	projects := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(projects); err != nil {
		return
	}
	results, err := session.ScanProjects(projects)
	if err != nil {
		s.log.Warn("discovery scan failed", "err", err)
		return
	}
	store, err := session.NewGlobalFileStore()
	if err != nil {
		s.log.Warn("discovery store init failed", "err", err)
		return
	}
	adopted, err := session.AdoptUnknown(store, results)
	if err != nil {
		s.log.Warn("discovery adopt failed", "err", err)
		return
	}
	if len(adopted) > 0 {
		names := make([]string, 0, len(adopted))
		for _, a := range adopted {
			names = append(names, a.Name)
			s.publishEvent(&daemonpb.RegistryEvent{
				Kind:        daemonpb.RegistryEvent_SESSION_ADOPTED,
				SessionName: a.Name,
				SessionId:   a.Metadata.SessionID,
			})
		}
		s.log.Info("discovery adopted sessions", "count", len(adopted), "names", names)
	} else {
		s.log.Debug("discovery scan: nothing new", "transcripts", len(results))
	}
}

// TriggerScan implements the RPC. The daemon's scanner runs whenever
// the request lands; the response carries any sessions adopted by the
// previous scan tick so the caller has immediate confirmation.
// Subscribers also receive a SESSION_ADOPTED event for each new entry.
func (s *Server) TriggerScan(ctx context.Context, _ *daemonpb.TriggerScanRequest) (*daemonpb.TriggerScanResponse, error) {
	select {
	case s.scanWake <- struct{}{}:
	default:
		// Channel is full; another wake is already pending.
	}
	return &daemonpb.TriggerScanResponse{}, nil
}

// SubscribeRegistry streams RegistryEvent values to the client until
// the client disconnects. Each subscriber gets its own buffered channel
// so a slow client cannot block the broadcaster. Events that arrive
// while a subscriber's buffer is full are dropped for that one client.
func (s *Server) SubscribeRegistry(_ *daemonpb.SubscribeRegistryRequest, stream daemonpb.AgentGateD_SubscribeRegistryServer) error {
	ch := make(chan *daemonpb.RegistryEvent, 32)

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
func (s *Server) RenameSession(_ context.Context, req *daemonpb.RenameSessionRequest) (*daemonpb.RenameSessionResponse, error) {
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
	s.publishEvent(&daemonpb.RegistryEvent{
		Kind:        daemonpb.RegistryEvent_SESSION_RENAMED,
		SessionName: req.NewName,
		OldName:     req.OldName,
	})
	s.log.Info("session renamed via RPC", "old", req.OldName, "new", req.NewName)
	return &daemonpb.RenameSessionResponse{}, nil
}

// DeleteSession is the daemon-side delete. It removes the session
// metadata from the registry and broadcasts SESSION_DELETED so all
// connected dashboards prune the row immediately. Transcript and
// agent log cleanup live in the cmd layer because they reach into
// per-project state outside the daemon's scope.
func (s *Server) DeleteSession(_ context.Context, req *daemonpb.DeleteSessionRequest) (*daemonpb.DeleteSessionResponse, error) {
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
	s.publishEvent(&daemonpb.RegistryEvent{
		Kind:        daemonpb.RegistryEvent_SESSION_DELETED,
		SessionName: req.Name,
	})
	s.log.Info("session deleted via RPC", "name", req.Name)
	return &daemonpb.DeleteSessionResponse{}, nil
}

// publishEvent fans an event out to every active subscriber. Slow
// subscribers whose buffer is full silently drop the event to keep the
// broadcaster non-blocking.
func (s *Server) publishEvent(ev *daemonpb.RegistryEvent) {
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
	s.mu.Unlock()

	if ok {
		_ = os.RemoveAll(config.SessionRuntimeDir(sess.wrapperID))
		s.log.Info("session released",
			"wrapper_id", req.WrapperId,
			"session", sess.sessionName,
			"model", sess.model,
			"active_sessions", remaining,
		)
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

	// Build prompt.
	// The summary should describe the session's TOPIC, not its current
	// status. Prior revisions asked "what is this session currently
	// working on", which Sonnet read as a live-state question and would
	// answer "Session ended with no active coding task" for sessions
	// whose last turn was idle. Asking for the topic produces a useful
	// label regardless of whether the conversation is still in flight.
	var promptParts []string
	promptParts = append(promptParts, "Write a topic label of AT MOST 5 words that names what this coding conversation is about. Pretend you are labeling a bookmark tab. Do not describe state. Do not mention whether it is finished. Do not mention message counts. Mention the concrete project, feature, or task by name when evident. Output ONLY the label. No preamble. No trailing punctuation.")
	promptParts = append(promptParts, "")
	promptParts = append(promptParts, "Good examples:")
	promptParts = append(promptParts, "Clotilde tcell TUI rewrite")
	promptParts = append(promptParts, "MWAN health-check CLI")
	promptParts = append(promptParts, "Tack node model schema")
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
	for _, m := range payload.Messages {
		promptParts = append(promptParts, sanitizePromptLine(m))
	}

	prompt := strings.Join(promptParts, "\n")

	// Call claude -p --model sonnet
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	claudeBin, err := findRealClaude()
	if err != nil {
		s.log.Warn("context update: claude not found", "err", err)
		return
	}

	// Run claude -p in a clotilde-owned scratch dir so the resulting
	// transcript file lands in ~/.claude/projects/<our scratch>/ instead
	// of ~/.claude/projects/-/ (the encoded form of /). Running from /
	// also caused macOS TCC prompts because claude tried to inspect
	// neighbouring system directories.
	// Anchor claude in a clotilde-owned scratch dir so the resulting
	// transcript file lands inside ~/Library/Caches/clotilde/... and
	// not in ~/.claude/projects/-/ (the encoded form of /). The
	// subprocess inherits the daemon's environment because claude
	// needs ANTHROPIC_API_KEY and the rest of its npm runtime.
	cmd := exec.CommandContext(ctx, claudeBin, "-p", "--model", "sonnet", prompt)
	if scratch := contextScratchDir(); scratch != "" {
		cmd.Dir = scratch
	}
	output, err := cmd.Output()
	if err != nil {
		s.log.Warn("context update: claude -p failed", "session", payload.SessionName, "err", err)
		return
	}

	summary := strings.TrimSpace(string(output))
	// Keep only the first line. Sonnet sometimes appends an explanation or
	// a trailing sentence despite the prompt. We treat the first line as
	// the label candidate.
	if idx := strings.IndexAny(summary, "\r\n"); idx >= 0 {
		summary = strings.TrimSpace(summary[:idx])
	}
	// Strip any surrounding quotes or trailing punctuation the model may add.
	summary = strings.Trim(summary, " \t`\"'")
	summary = strings.TrimRight(summary, ".!?")
	// Reject anything that is not a short label. The column shows five
	// words; we allow a little slack for punctuation but not a sentence.
	words := strings.Fields(summary)
	if len(summary) == 0 || len(summary) > 60 || len(words) > 7 {
		s.log.Warn("context update: bad summary",
			"session", payload.SessionName,
			"len", len(summary),
			"words", len(words),
			"value", summary)
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
	// Stamp the message count so the TUI can detect when this summary
	// becomes stale. Messages may have been filtered or truncated by
	// the caller; an approximate count is fine for the staleness check.
	sess.Metadata.ContextMessageCount = len(payload.Messages)
	if err := store.Update(sess); err != nil {
		s.log.Warn("context update: update failed", "session", payload.SessionName, "err", err)
		return
	}

	s.log.Info("context updated", "session", payload.SessionName, "context", summary)
}

var (
	imageMarkerRe  = regexp.MustCompile(`(?i)\[image[^\]]*\]`)
	absolutePathRe = regexp.MustCompile(`/(?:Users|Volumes|System|Library)/[^\s\]]+`)
)

// sanitizePromptLine strips image markers and absolute file paths from
// a message line before it lands in the labeling prompt. claude -p
// otherwise interprets entries like "[Image #4]" or
// "/Users/.../Photos Library.photoslibrary/foo" as files to load,
// triggering macOS TCC prompts for whatever protected directory they
// reference. The replacement keeps the surrounding text intact so the
// model still has signal about the conversation topic.
func sanitizePromptLine(line string) string {
	out := line
	out = imageMarkerRe.ReplaceAllString(out, "[image]")
	out = absolutePathRe.ReplaceAllString(out, "<path>")
	return out
}

// contextScratchDir returns a clotilde-owned working directory that
// the daemon uses as cwd when invoking claude -p for context summaries.
// Anchoring claude there keeps its transcript files inside the scratch
// project and stops them from polluting ~/.claude/projects/-/. The dir
// is created lazily and cached for the life of the process.
var (
	scratchDirOnce sync.Once
	scratchDirPath string
)

func contextScratchDir() string {
	scratchDirOnce.Do(func() {
		base, err := os.UserCacheDir()
		if err != nil {
			return
		}
		dir := filepath.Join(base, "clotilde", "context-scratch")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
		scratchDirPath = dir
	})
	return scratchDirPath
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

// ListActiveSessions returns all currently acquired sessions.
func (s *Server) ListActiveSessions(_ context.Context, _ *daemonpb.ListActiveSessionsRequest) (*daemonpb.ListActiveSessionsResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var active []*daemonpb.ActiveSession
	for wid, sess := range s.sessions {
		active = append(active, &daemonpb.ActiveSession{
			SessionName: sess.sessionName,
			WrapperId:   wid,
		})
	}

	return &daemonpb.ListActiveSessionsResponse{Sessions: active}, nil
}
