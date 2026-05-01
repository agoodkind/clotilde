package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DiscoveryResult captures the outcome of a single transcript discovery.
// The TranscriptPath is always populated; the rest depend on whether the
// first entry could be parsed.
type DiscoveryResult struct {
	TranscriptPath string
	SessionID      string
	WorkspaceRoot  string
	Entrypoint     string
	FirstEntryTime time.Time
	CustomTitle    string // user-given chat name from Claude Code "custom-title" entries
	ForkParentID   string // parent session UUID when this transcript was created as a fork
	IsAutoName     bool   // SDK-CLI invocation that looks like a clyde auto-name call
	IsForked       bool   // transcript carries fork lineage from Claude Code
	IsSubagent     bool   // file lives in a subagents/ directory
}

// AdoptedSession is the registry entry created for a previously-unknown
// transcript. It includes the auto-generated name so callers can report.
type AdoptedSession struct {
	Name     string
	Metadata Metadata
}

// scratchDirSuffixes lists workspace-root path fragments produced by
// clyde-internal subprocess invocations. Discovery skips any
// transcript whose cwd matches one of these so the user's session
// list never fills with adapter or context-summary noise.
var scratchDirSuffixes = []string{
	"/Library/Caches/clotilde/context-scratch",
	"/.cache/clotilde/context-scratch",
	"/Library/Caches/clotilde/adapter-scratch",
	"/.cache/clotilde/adapter-scratch",
	"/Library/Caches/clyde/context-scratch",
	"/.cache/clyde/context-scratch",
	"/Library/Caches/clyde/adapter-scratch",
	"/.cache/clyde/adapter-scratch",
}

// isClydeScratch reports whether path looks like a clyde owned
// scratch directory used to anchor internal claude -p calls. The
// match is suffix based so it works whether the user's home is at
// /Users/foo or /home/foo or anywhere else.
func isClydeScratch(path string) bool {
	if path == "" {
		return false
	}
	for _, s := range scratchDirSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// transcriptHeader is the minimum subset we need from the first entry of a
// jsonl transcript to map it into the registry.
type transcriptHeader struct {
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Entrypoint  string `json:"entrypoint"`
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	Content     string `json:"content"`     // present on queue-operation entries
	CustomTitle string `json:"customTitle"` // present on custom-title entries
	ForkedFrom  struct {
		SessionID string `json:"sessionId"`
	} `json:"forkedFrom"`
}

// ScanProjects walks ~/.claude/projects/<encoded-cwd>/*.jsonl and returns
// one DiscoveryResult per transcript. Subagent transcripts (anywhere under
// a subagents/ directory) are flagged but still returned so callers can
// decide whether to surface them. The walk is best-effort: unreadable
// files are skipped silently.
func ScanProjects(claudeProjectsDir string) ([]DiscoveryResult, error) {
	started := time.Now()
	var out []DiscoveryResult
	var withTitle int
	err := filepath.WalkDir(claudeProjectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip permission errors but keep walking other branches.
			if os.IsPermission(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		dr, ok := readTranscriptHeader(path)
		if !ok {
			return nil
		}
		if dr.CustomTitle != "" {
			withTitle++
		}
		out = append(out, dr)
		return nil
	})
	if err != nil {
		slog.Warn("session.scan.walk_failed",
			"component", "session",
			"subcomponent", "scan",
			"projects_dir", claudeProjectsDir,
			"err", err,
		)
		return nil, err
	}
	slog.Debug("session.scan.completed",
		"component", "session",
		"subcomponent", "scan",
		"projects_dir", claudeProjectsDir,
		"transcripts", len(out),
		"with_custom_title", withTitle,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return out, nil
}

// ReadTranscriptHeader is the exported entry point for
// readTranscriptHeader, used by hook and other callers outside the
// package to learn the sessionId, workspace root, and customTitle of a
// transcript on disk without walking the full projects directory.
func ReadTranscriptHeader(path string) (DiscoveryResult, bool) {
	return readTranscriptHeader(path)
}

// readTranscriptHeader reads enough of a jsonl transcript to identify the
// session it belongs to. The function returns ok=false when the file is
// unreadable or contains no recognizable entries.
func readTranscriptHeader(path string) (DiscoveryResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveryResult{}, false
	}
	defer f.Close()

	dr := DiscoveryResult{TranscriptPath: path}
	if strings.Contains(path, string(os.PathSeparator)+"subagents"+string(os.PathSeparator)) {
		dr.IsSubagent = true
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var h transcriptHeader
		if err := json.Unmarshal(line, &h); err != nil {
			continue
		}
		// queue-operation entries come first in clyde-wrapped sessions
		// and carry the auto-name prompt as their content. They never
		// have a cwd or entrypoint so they cannot stand alone.
		if h.Type == "queue-operation" {
			if !dr.IsAutoName && looksLikeAutoNamePrompt(h.Content) {
				dr.IsAutoName = true
			}
			continue
		}
		// custom-title entries carry the user-given chat name. They may
		// appear on line 1 of user-named transcripts, or later when a
		// name is set or changed mid-session. The latest value wins so
		// DisplayTitle reflects the current Claude Code name.
		if h.Type == "custom-title" {
			if h.CustomTitle != "" {
				dr.CustomTitle = h.CustomTitle
			}
			if h.SessionID != "" && dr.SessionID == "" {
				dr.SessionID = h.SessionID
			}
			if h.ForkedFrom.SessionID != "" && dr.ForkParentID == "" {
				dr.ForkParentID = h.ForkedFrom.SessionID
				dr.IsForked = true
			}
			continue
		}
		if h.SessionID != "" && dr.SessionID == "" {
			dr.SessionID = h.SessionID
		}
		if h.ForkedFrom.SessionID != "" && dr.ForkParentID == "" {
			dr.ForkParentID = h.ForkedFrom.SessionID
			dr.IsForked = true
		}
		if h.CWD != "" && dr.WorkspaceRoot == "" {
			dr.WorkspaceRoot = h.CWD
		}
		if h.Entrypoint != "" && dr.Entrypoint == "" {
			dr.Entrypoint = h.Entrypoint
		}
		if h.Timestamp != "" && dr.FirstEntryTime.IsZero() {
			if t, err := time.Parse(time.RFC3339, h.Timestamp); err == nil {
				dr.FirstEntryTime = t
			}
		}
		if dr.SessionID != "" && dr.WorkspaceRoot != "" && dr.Entrypoint != "" && !dr.FirstEntryTime.IsZero() {
			break
		}
	}
	if dr.SessionID == "" {
		return DiscoveryResult{}, false
	}
	if dr.Entrypoint == "sdk-cli" {
		dr.IsAutoName = true
	}
	return dr, true
}

// looksLikeAutoNamePrompt heuristically detects the prompts clyde
// dispatches to haiku for session naming. The prompt always asks for a
// kebab-case label and includes the words "kebab-case" and "Output ONLY".
func looksLikeAutoNamePrompt(content string) bool {
	if content == "" {
		return false
	}
	c := strings.ToLower(content)
	return strings.Contains(c, "kebab-case") && strings.Contains(c, "output only")
}

// AdoptUnknown creates registry stubs for transcripts that no existing
// session knows about. Sessions that are tagged as auto-name or subagent
// are skipped so the dashboard does not fill with noise. The function
// returns the list of adopted sessions.
func AdoptUnknown(store *FileStore, results []DiscoveryResult) ([]AdoptedSession, error) {
	known, err := buildKnownUUIDSet(store)
	if err != nil {
		slog.Warn("session.adopt.known_uuids_failed",
			"component", "session",
			"subcomponent", "adopt",
			"err", err,
		)
		return nil, err
	}
	existingNames, err := buildExistingNameSet(store)
	if err != nil {
		slog.Warn("session.adopt.existing_names_failed",
			"component", "session",
			"subcomponent", "adopt",
			"err", err,
		)
		return nil, err
	}

	ordered := append([]DiscoveryResult(nil), results...)
	sort.SliceStable(ordered, func(i int, j int) bool {
		left := ordered[i]
		right := ordered[j]
		if left.SessionID == right.SessionID {
			if left.CustomTitle != "" && right.CustomTitle == "" {
				return true
			}
			if right.CustomTitle != "" && left.CustomTitle == "" {
				return false
			}
			if !left.FirstEntryTime.Equal(right.FirstEntryTime) {
				return left.FirstEntryTime.After(right.FirstEntryTime)
			}
			if left.WorkspaceRoot != "" && right.WorkspaceRoot == "" {
				return true
			}
			if right.WorkspaceRoot != "" && left.WorkspaceRoot == "" {
				return false
			}
		}
		if left.SessionID != right.SessionID {
			return left.SessionID < right.SessionID
		}
		return left.TranscriptPath < right.TranscriptPath
	})

	slog.Debug("session.adopt.started",
		"component", "session",
		"subcomponent", "adopt",
		"candidates", len(ordered),
		"known_uuids", len(known),
		"existing_names", len(existingNames),
	)

	var adopted []AdoptedSession
	skippedAutoOrSubagent := 0
	skippedScratch := 0
	skippedNoSessionID := 0
	skippedKnown := 0
	createFailed := 0
	for _, r := range ordered {
		if r.IsAutoName || r.IsSubagent {
			skippedAutoOrSubagent++
			continue
		}
		if isClydeScratch(r.WorkspaceRoot) {
			skippedScratch++
			continue
		}
		if r.SessionID == "" {
			skippedNoSessionID++
			continue
		}
		if _, ok := known[r.SessionID]; ok {
			skippedKnown++
			continue
		}
		name, nameSource := pickAdoptedName(r, existingNames)
		existingNames[name] = true

		md := Metadata{
			Name:            name,
			SessionID:       r.SessionID,
			TranscriptPath:  r.TranscriptPath,
			WorkspaceRoot:   r.WorkspaceRoot,
			WorkDir:         r.WorkspaceRoot,
			DisplayTitle:    r.CustomTitle,
			IsForkedSession: r.IsForked,
		}
		if r.IsForked && r.ForkParentID != "" {
			if parentName, ok := known[r.ForkParentID]; ok {
				md.ParentSession = parentName
			}
		}
		fi, err := os.Stat(r.TranscriptPath)
		if err == nil {
			md.LastAccessed = fi.ModTime()
		}
		switch {
		case !r.FirstEntryTime.IsZero():
			md.Created = r.FirstEntryTime
		case !md.LastAccessed.IsZero():
			md.Created = md.LastAccessed
		default:
			md.Created = time.Now()
		}
		if md.LastAccessed.IsZero() {
			md.LastAccessed = md.Created
		}

		sess := &Session{Name: name, Metadata: md}
		if err := store.Create(sess); err != nil {
			createFailed++
			slog.Warn("session.adopt.create_failed",
				"component", "session",
				"subcomponent", "adopt",
				"session", name,
				"session_id", r.SessionID,
				"transcript", r.TranscriptPath,
				"err", err,
			)
			continue
		}
		slog.Debug("session.adopt.created",
			"component", "session",
			"subcomponent", "adopt",
			"session", name,
			"session_id", r.SessionID,
			"forked", md.IsForkedSession,
			"parent_session", md.ParentSession,
			"transcript", r.TranscriptPath,
			"workspace", r.WorkspaceRoot,
			"name_source", nameSource,
			"display_title", r.CustomTitle,
		)
		adopted = append(adopted, AdoptedSession{Name: name, Metadata: md})
		known[r.SessionID] = name
	}
	slog.Debug("session.adopt.completed",
		"component", "session",
		"subcomponent", "adopt",
		"adopted", len(adopted),
		"considered", len(ordered),
		"skipped_auto_or_subagent", skippedAutoOrSubagent,
		"skipped_clyde_scratch", skippedScratch,
		"skipped_no_session_id", skippedNoSessionID,
		"skipped_already_known", skippedKnown,
		"create_failed", createFailed,
	)
	return adopted, nil
}

// pickAdoptedName chooses a session name for an adopted transcript. It
// prefers the sanitized Claude Code customTitle so clyde verbs accept
// the user-given chat name directly. Collisions with existing names are
// resolved with UniqueName. When customTitle is absent or sanitizes to
// empty (for example an emoji-only title) the function falls back to
// the workspace-plus-UUID scheme in uniqueAdoptedName. The second return
// value is a short label of the source used, for structured logs.
func pickAdoptedName(r DiscoveryResult, taken map[string]bool) (string, string) {
	if sanitized := Sanitize(r.CustomTitle); sanitized != "" {
		candidate := UniqueName(sanitized, taken)
		if candidate != "" && ValidateName(candidate) == nil {
			slog.Debug("session.adopt.name_picked",
				"component", "session",
				"subcomponent", "adopt",
				"session_id", r.SessionID,
				"source", "custom_title",
				"raw_title", r.CustomTitle,
				"name", candidate,
			)
			return candidate, "custom_title"
		}
		slog.Debug("session.adopt.name_sanitize_unusable",
			"component", "session",
			"subcomponent", "adopt",
			"session_id", r.SessionID,
			"raw_title", r.CustomTitle,
			"sanitized", sanitized,
		)
	}
	fallback := uniqueAdoptedName(r, taken)
	slog.Debug("session.adopt.name_picked",
		"component", "session",
		"subcomponent", "adopt",
		"session_id", r.SessionID,
		"source", "workspace_uuid_fallback",
		"raw_title", r.CustomTitle,
		"name", fallback,
	)
	return fallback, "workspace_uuid_fallback"
}

// buildKnownUUIDSet returns the set of UUIDs the store already manages.
// Both current and previous IDs are included so a session that has gone
// through /clear cycles is not double-adopted.
func buildKnownUUIDSet(store *FileStore) (map[string]string, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(all)*2)
	for _, s := range all {
		if s.Metadata.SessionID != "" {
			out[s.Metadata.SessionID] = s.Name
		}
		for _, id := range s.Metadata.PreviousSessionIDs {
			out[id] = s.Name
		}
	}
	return out, nil
}

func buildExistingNameSet(store *FileStore) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, s := range all {
		out[s.Name] = true
	}
	return out, nil
}

// uniqueAdoptedName generates a registry-safe name for an adopted
// transcript. The base is a sanitized basename of the workspace root
// joined with the first eight characters of the session UUID. Collisions
// are resolved with the shared UniqueName helper.
func uniqueAdoptedName(r DiscoveryResult, taken map[string]bool) string {
	base := workspaceBaseName(r.WorkspaceRoot)
	short := safeShortUUID(r.SessionID)
	return UniqueName(fmt.Sprintf("%s-%s", base, short), taken)
}

func workspaceBaseName(root string) string {
	if root == "" {
		return "adopted"
	}
	base := filepath.Base(root)
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "adopted"
	}
	return b.String()
}

func safeShortUUID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
