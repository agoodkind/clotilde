package session

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/util"
)

// Compile-time check that FileStore implements Store.
var _ Store = (*FileStore)(nil)

const (
	metadataFile = "metadata.json"
	settingsFile = "settings.json"
)

// Store defines the interface for session storage operations.
type Store interface {
	// List returns all sessions, sorted by lastAccessed (most recent first)
	List() ([]*Session, error)

	// ListForWorkspace returns sessions whose WorkspaceRoot matches the given path,
	// sorted by lastAccessed (most recent first).
	ListForWorkspace(workspaceRoot string) ([]*Session, error)

	// Get retrieves a session by name
	Get(name string) (*Session, error)

	// Rename renames a session: moves the directory, updates metadata Name field,
	// and updates any child sessions whose ParentSession matches oldName.
	Rename(oldName, newName string) error

	// Search returns sessions matching query against name, UUID,
	// and context (case-insensitive substring match).
	Search(query string) ([]*Session, error)

	// Create creates a new session folder structure with metadata
	Create(session *Session) error

	// Update updates session metadata
	Update(session *Session) error

	// Delete removes a session folder and all its contents
	Delete(name string) error

	// Resolve finds a session using a multi-tier lookup:
	// 1. Exact name match
	// 2. UUID match (checks SessionID and PreviousSessionIDs)
	// 3. Substring search (returns single match only)
	// 4. Transparent adoption: scans ~/.claude/projects for a transcript
	//    whose sessionId or sanitized customTitle matches query, then
	//    registers a clyde session stub (metadata.json) and returns it.
	//    This tier is skipped on FileStores constructed with
	//    NewFileStoreReadOnly (for scan paths that must not recurse).
	// Returns (nil, nil) if no match is found.
	Resolve(query string) (*Session, error)

	// Exists checks if a session exists
	Exists(name string) bool

	// LoadSettings loads settings.json for a session (returns nil if not exists)
	LoadSettings(name string) (*Settings, error)

	// SaveSettings saves settings.json for a session
	SaveSettings(name string, settings *Settings) error
}

// FileStore implements Store using the filesystem.
type FileStore struct {
	clydeRoot string

	// discoveryCache memoizes ScanProjects results so tier-4 Resolve
	// misses do not re-walk ~/.claude/projects on every call. nil when
	// auto-adoption is disabled (NewFileStoreReadOnly or NewFileStore).
	discoveryCache *discoveryCache

	// noAdopt disables tier 4 even when a cache is present. Used by
	// internal scan paths (prune, discovery scanner) that must not
	// trigger a recursive adopt while they are iterating transcripts.
	noAdopt bool
}

// NewFileStore creates a new FileStore without tier-4 auto-adoption.
// Used by tests and by code paths that construct a store for a
// non-global clyde root where scanning ~/.claude/projects is not
// meaningful.
func NewFileStore(clydeRoot string) *FileStore {
	return &FileStore{
		clydeRoot: clydeRoot,
	}
}

// NewFileStoreReadOnly returns a FileStore that never triggers tier 4
// adoption from Resolve. Call sites inside the scan and prune pipeline
// use this to prevent a Resolve performed during scan from re-entering
// scan and recursing. The returned store reads and writes like a normal
// FileStore; only the transparent-adoption side effect is suppressed.
func NewFileStoreReadOnly(clydeRoot string) *FileStore {
	return &FileStore{
		clydeRoot: clydeRoot,
		noAdopt:   true,
	}
}

// NewFileStoreWithDiscovery returns a FileStore rooted at clydeRoot
// with tier-4 adoption wired to scan projectsDir. This is the explicit
// form used by tests and by any caller that wants tier 4 enabled
// against a non-default projects directory. The production code path
// uses NewGlobalFileStore, which resolves projectsDir from $HOME.
func NewFileStoreWithDiscovery(clydeRoot, projectsDir string) *FileStore {
	return &FileStore{
		clydeRoot:      clydeRoot,
		discoveryCache: newDiscoveryCache(projectsDir, nil, 0),
	}
}

// NewGlobalFileStore creates a FileStore pointing at the global sessions directory.
// Creates the directory if it doesn't exist. The returned store has the
// tier-4 discovery cache attached so Resolve transparently adopts any
// Claude Code session present on disk but not yet registered with clyde.
func NewGlobalFileStore() (*FileStore, error) {
	if err := config.EnsureGlobalSessionsDir(); err != nil {
		return nil, fmt.Errorf("failed to create global sessions directory: %w", err)
	}
	fs := &FileStore{clydeRoot: config.GlobalDataDir()}
	if home, err := os.UserHomeDir(); err == nil {
		fs.discoveryCache = newDiscoveryCache(config.ClaudeProjectsRoot(home), nil, 0)
	} else {
		slog.Warn("session.store.home_dir_failed",
			"component", "session",
			"subcomponent", "store",
			slog.Any("err", err),
		)
	}
	return fs, nil
}

// NewGlobalFileStoreReadOnly returns a NewGlobalFileStore variant with
// tier 4 disabled. Scan and prune paths use this to avoid recursive
// adoption while they walk transcripts directly.
func NewGlobalFileStoreReadOnly() (*FileStore, error) {
	if err := config.EnsureGlobalSessionsDir(); err != nil {
		return nil, fmt.Errorf("failed to create global sessions directory: %w", err)
	}
	return &FileStore{clydeRoot: config.GlobalDataDir(), noAdopt: true}, nil
}

// List returns all sessions, sorted by lastAccessed (most recent first).
func (fs *FileStore) List() ([]*Session, error) {
	sessionsDir := config.GetSessionsDir(fs.clydeRoot)

	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []*Session{}, nil
		}
		return nil, err
	}

	var sessions []*Session
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		session, err := fs.Get(entry.Name())
		if err != nil {
			// Skip sessions that can't be loaded
			continue
		}
		sessions = append(sessions, session)
	}

	// Sort by lastAccessed (most recent first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Metadata.LastAccessed.After(sessions[j].Metadata.LastAccessed)
	})

	return sessions, nil
}

// ListForWorkspace returns sessions whose WorkspaceRoot matches the given path,
// sorted by lastAccessed (most recent first).
func (fs *FileStore) ListForWorkspace(workspaceRoot string) ([]*Session, error) {
	all, err := fs.List()
	if err != nil {
		return nil, err
	}

	var result []*Session
	for _, sess := range all {
		if sess.Metadata.WorkspaceRoot == workspaceRoot {
			result = append(result, sess)
		}
	}
	return result, nil
}

// Get retrieves a session by name.
func (fs *FileStore) Get(name string) (*Session, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, name)
	if !util.DirExists(sessionDir) {
		return nil, fmt.Errorf("session '%s' not found", name)
	}

	metadataPath := filepath.Join(sessionDir, metadataFile)
	var metadata Metadata
	if err := util.ReadJSON(metadataPath, &metadata); err != nil {
		return nil, fmt.Errorf("failed to read session metadata: %w", err)
	}

	return &Session{
		Name:     name,
		Metadata: metadata,
	}, nil
}

// Search returns sessions matching query against name, UUID,
// and context (case-insensitive substring match).
func (fs *FileStore) Search(query string) ([]*Session, error) {
	sessions, err := fs.List()
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(query)
	var matches []*Session
	for _, sess := range sessions {
		if strings.Contains(strings.ToLower(sess.Name), q) ||
			strings.Contains(strings.ToLower(sess.Metadata.SessionID), q) ||
			strings.Contains(strings.ToLower(sess.Metadata.Context), q) {
			matches = append(matches, sess)
		}
	}
	return matches, nil
}

// Resolve finds a session using a multi-tier lookup strategy.
// Returns (nil, nil) if no match is found (not an error, caller decides behavior).
func (fs *FileStore) Resolve(query string) (*Session, error) {
	if query == "" {
		return nil, nil
	}
	if sess := fs.resolveFromStore(query); sess != nil {
		return sess, nil
	}

	if fs.noAdopt || fs.discoveryCache == nil {
		slog.Debug("session.resolve.miss",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"tier4", "disabled",
			"reason", adoptDisabledReason(fs),
		)
		return nil, nil
	}

	slog.Debug("session.resolve.tier4_started",
		"component", "session",
		"subcomponent", "resolve",
		"query", query,
	)
	adopted, err := fs.adoptFromDiscovery(query)
	if err != nil {
		slog.Warn("session.resolve.tier4_failed",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			slog.Any("err", err),
		)
		return nil, nil
	}
	if adopted == nil {
		slog.Debug("session.resolve.tier4_no_match",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
		)
		return nil, nil
	}
	slog.Info("session.resolve.tier4_adopted",
		"component", "session",
		"subcomponent", "resolve",
		"query", query,
		"session", adopted.Name,
		"session_id", adopted.Metadata.SessionID,
		"display_title", adopted.Metadata.DisplayTitle,
	)
	// Re-run tiers 1-3 once against the now-adopted set. In the common
	// case tier 1 will hit directly because the adopted Name equals the
	// query. When the query was a UUID the adopted Name differs; tier 2
	// picks it up.
	if sess := fs.resolveFromStore(query); sess != nil {
		return sess, nil
	}
	// Fallback: the adopter's Name may not exact-match the query string
	// (for example the query was a bare UUID and tier 2 cannot locate
	// the session because the cached List is now stale). Return the
	// freshly adopted session directly; its metadata is authoritative.
	return adopted, nil
}

// resolveFromStore performs tiers 1-3 without attempting adoption. The
// tier-4 path calls this both before and after adopting so the adopted
// entry is surfaced through the same lookup chain the caller would have
// hit if the session had always been registered.
func (fs *FileStore) resolveFromStore(query string) *Session {
	// Tier 1: exact name match
	if sess, err := fs.Get(query); err == nil {
		slog.Debug("session.resolve.tier1_hit",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"session", sess.Name,
		)
		return sess
	}

	// Tier 2: UUID match
	if _, parseErr := uuid.Parse(query); parseErr == nil {
		sessions, listErr := fs.List()
		if listErr == nil {
			for _, sess := range sessions {
				if sess.Metadata.SessionID == query {
					slog.Debug("session.resolve.tier2_hit",
						"component", "session",
						"subcomponent", "resolve",
						"query", query,
						"session", sess.Name,
						"match", "current_uuid",
					)
					return sess
				}
				if slices.Contains(sess.Metadata.PreviousSessionIDs, query) {
					slog.Debug("session.resolve.tier2_hit",
						"component", "session",
						"subcomponent", "resolve",
						"query", query,
						"session", sess.Name,
						"match", "previous_uuid",
					)
					return sess
				}
			}
		}
	}

	// Tier 3: substring search (single match only)
	matches, err := fs.Search(query)
	if err != nil || len(matches) == 0 {
		return nil
	}
	if len(matches) == 1 {
		slog.Debug("session.resolve.tier3_hit",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"session", matches[0].Name,
		)
		return matches[0]
	}
	slog.Debug("session.resolve.tier3_ambiguous",
		"component", "session",
		"subcomponent", "resolve",
		"query", query,
		"match_count", len(matches),
	)
	return nil
}

// adoptFromDiscovery runs the tier-4 scan and adoption loop. It asks
// the discovery cache for the current transcript list, looks for an
// entry whose sessionId or sanitized customTitle matches query, runs
// AdoptUnknown to materialize a metadata.json, and invalidates the
// cache so a subsequent miss does not see the same transcript as
// unknown. Returns the adopted session, or nil when no transcript
// matches the query.
func (fs *FileStore) adoptFromDiscovery(query string) (*Session, error) {
	results, err := fs.discoveryCache.Get()
	if err != nil {
		return nil, fmt.Errorf("discovery scan: %w", err)
	}

	queryIsUUID := false
	if _, parseErr := uuid.Parse(query); parseErr == nil {
		queryIsUUID = true
	}

	var match *DiscoveryResult
	for i := range results {
		r := &results[i]
		if r.IsAutoName || r.IsSubagent || r.SessionID == "" {
			continue
		}
		if queryIsUUID && r.SessionID == query {
			match = r
			break
		}
		if !queryIsUUID {
			if sanitized := Sanitize(r.CustomTitle); sanitized != "" && sanitized == query {
				match = r
				break
			}
		}
	}
	if match == nil {
		slog.Debug("session.resolve.tier4_scan_empty",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"query_is_uuid", queryIsUUID,
			"scanned", len(results),
		)
		return nil, nil
	}

	slog.Debug("session.resolve.tier4_scan_matched",
		"component", "session",
		"subcomponent", "resolve",
		"query", query,
		"query_is_uuid", queryIsUUID,
		"session_id", match.SessionID,
		"transcript", match.TranscriptPath,
		"raw_custom_title", match.CustomTitle,
	)

	// If the UUID is already registered (for example the daemon's
	// background scanner adopted it under an auto-generated name before
	// the user assigned a customTitle), reconcile the existing session
	// to match the Claude Code title rather than creating a duplicate.
	// The rename uses the sanitized customTitle so tier 1 finds it on
	// the retry. DisplayTitle is backfilled unconditionally.
	if existing := fs.findByUUID(match.SessionID); existing != nil {
		return fs.reconcileExisting(existing, match, query)
	}

	adopted, adoptErr := AdoptUnknown(fs, []DiscoveryResult{*match})
	fs.discoveryCache.Invalidate()
	if adoptErr != nil {
		return nil, fmt.Errorf("adopt: %w", adoptErr)
	}
	if len(adopted) == 0 {
		// AdoptUnknown skipped the candidate (race with another process
		// that adopted it just now, or scratch/subagent filter fired).
		// Try one more read from the store before giving up.
		slog.Debug("session.resolve.tier4_adopt_skipped",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"session_id", match.SessionID,
		)
		if sess := fs.resolveFromStore(query); sess != nil {
			return sess, nil
		}
		return nil, nil
	}

	stub := adopted[0]
	return &Session{Name: stub.Name, Metadata: stub.Metadata}, nil
}

// adoptDisabledReason labels why tier 4 was skipped, for structured
// logs. Keeps the skip branch in Resolve a single slog call.
func adoptDisabledReason(fs *FileStore) string {
	if fs.noAdopt {
		return "read_only_store"
	}
	if fs.discoveryCache == nil {
		return "no_discovery_cache"
	}
	return "unknown"
}

// findByUUID returns the first registered session whose SessionID or
// PreviousSessionIDs contain uuid, or nil when none match. Used by
// tier 4 to detect a session that was previously auto-adopted (by the
// daemon's background scanner or the hook) under a name that does not
// reflect the user's customTitle.
func (fs *FileStore) findByUUID(uuid string) *Session {
	if uuid == "" {
		return nil
	}
	sessions, err := fs.List()
	if err != nil {
		slog.Warn("session.resolve.find_by_uuid_list_failed",
			"component", "session",
			"subcomponent", "resolve",
			"uuid", uuid,
			slog.Any("err", err),
		)
		return nil
	}
	for _, sess := range sessions {
		if sess.Metadata.SessionID == uuid {
			return sess
		}
		if slices.Contains(sess.Metadata.PreviousSessionIDs, uuid) {
			return sess
		}
	}
	return nil
}

// reconcileExisting updates an already-adopted session to reflect the
// Claude Code customTitle seen on the transcript. If the sanitized
// customTitle is a valid unique name, the session directory is renamed
// so tier-1 lookups by the customTitle succeed. DisplayTitle is
// backfilled unconditionally so the TUI shows the user-given title
// even when a rename is not possible. The function returns the session
// in its final state (post-rename), or the original when no rename was
// needed.
func (fs *FileStore) reconcileExisting(existing *Session, match *DiscoveryResult, query string) (*Session, error) {
	// Backfill DisplayTitle when the scan picked up a customTitle that
	// the existing metadata lacks. Persist immediately so subsequent
	// callers see the update regardless of the rename outcome.
	titleChanged := false
	if match.CustomTitle != "" && existing.Metadata.DisplayTitle != match.CustomTitle {
		existing.Metadata.DisplayTitle = match.CustomTitle
		titleChanged = true
	}
	if titleChanged {
		if err := fs.Update(existing); err != nil {
			slog.Warn("session.resolve.display_title_backfill_failed",
				"component", "session",
				"subcomponent", "resolve",
				"session", existing.Name,
				"session_id", existing.Metadata.SessionID,
				slog.Any("err", err),
			)
		} else {
			slog.Info("session.resolve.display_title_backfilled",
				"component", "session",
				"subcomponent", "resolve",
				"session", existing.Name,
				"session_id", existing.Metadata.SessionID,
				"display_title", match.CustomTitle,
			)
		}
	}

	sanitized := Sanitize(match.CustomTitle)
	if sanitized == "" || sanitized == existing.Name {
		slog.Debug("session.resolve.reconcile_no_rename",
			"component", "session",
			"subcomponent", "resolve",
			"query", query,
			"session", existing.Name,
			"sanitized", sanitized,
			"reason_empty", sanitized == "",
		)
		return existing, nil
	}

	// Pick a unique target name so a customTitle collision with another
	// session does not fail the rename. This reuses the same collision
	// strategy AdoptUnknown uses during fresh adoption.
	names, err := buildExistingNameSet(fs)
	if err != nil {
		slog.Warn("session.resolve.reconcile_name_set_failed",
			"component", "session",
			"subcomponent", "resolve",
			slog.Any("err", err),
		)
		return existing, nil
	}
	delete(names, existing.Name)
	target := UniqueName(sanitized, names)
	if target == "" || target == existing.Name {
		return existing, nil
	}

	if err := fs.Rename(existing.Name, target); err != nil {
		slog.Warn("session.resolve.reconcile_rename_failed",
			"component", "session",
			"subcomponent", "resolve",
			"old_name", existing.Name,
			"new_name", target,
			"session_id", existing.Metadata.SessionID,
			slog.Any("err", err),
		)
		return existing, nil
	}
	slog.Info("session.resolve.reconcile_renamed",
		"component", "session",
		"subcomponent", "resolve",
		"old_name", existing.Name,
		"new_name", target,
		"session_id", existing.Metadata.SessionID,
		"display_title", match.CustomTitle,
		"query", query,
	)
	renamed, err := fs.Get(target)
	if err != nil {
		return existing, nil
	}
	return renamed, nil
}

// Rename renames a session: moves the directory, updates metadata Name field,
// and updates any child sessions whose ParentSession matches oldName.
func (fs *FileStore) Rename(oldName, newName string) error {
	if err := ValidateName(oldName); err != nil {
		return fmt.Errorf("invalid old name: %w", err)
	}
	if err := ValidateName(newName); err != nil {
		return fmt.Errorf("invalid new name: %w", err)
	}
	if !fs.Exists(oldName) {
		return fmt.Errorf("session '%s' not found", oldName)
	}
	if fs.Exists(newName) {
		return fmt.Errorf("session '%s' already exists", newName)
	}

	oldDir := config.GetSessionDir(fs.clydeRoot, oldName)
	newDir := config.GetSessionDir(fs.clydeRoot, newName)
	if err := os.Rename(oldDir, newDir); err != nil {
		return fmt.Errorf("rename session directory: %w", err)
	}

	// Update metadata Name field in the new location
	sess, err := fs.Get(newName)
	if err != nil {
		return fmt.Errorf("failed to read renamed session: %w", err)
	}
	sess.Name = newName
	sess.Metadata.Name = newName
	if err := fs.Update(sess); err != nil {
		return fmt.Errorf("failed to update session metadata: %w", err)
	}

	// Update any child sessions that reference oldName as their parent
	sessions, err := fs.List()
	if err != nil {
		return nil // non-fatal: rename succeeded, parent references not updated
	}
	for _, child := range sessions {
		if child.Metadata.ParentSession == oldName {
			child.Metadata.ParentSession = newName
			_ = fs.Update(child) // best-effort
		}
	}

	return nil
}

// Create creates a new session folder structure with metadata.
func (fs *FileStore) Create(session *Session) error {
	if err := ValidateName(session.Name); err != nil {
		return err
	}

	if fs.Exists(session.Name) {
		return fmt.Errorf("session '%s' already exists", session.Name)
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, session.Name)
	if err := util.EnsureDir(sessionDir); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	metadataPath := filepath.Join(sessionDir, metadataFile)
	if err := util.WriteJSON(metadataPath, session.Metadata); err != nil {
		return fmt.Errorf("failed to write session metadata: %w", err)
	}

	return nil
}

// Update updates session metadata.
func (fs *FileStore) Update(session *Session) error {
	if err := ValidateName(session.Name); err != nil {
		return err
	}

	if !fs.Exists(session.Name) {
		return fmt.Errorf("session '%s' not found", session.Name)
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, session.Name)
	metadataPath := filepath.Join(sessionDir, metadataFile)
	if err := util.WriteJSON(metadataPath, session.Metadata); err != nil {
		return fmt.Errorf("failed to update session metadata: %w", err)
	}

	return nil
}

// Delete removes a session folder and all its contents.
func (fs *FileStore) Delete(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	if !fs.Exists(name) {
		return fmt.Errorf("session '%s' not found", name)
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, name)
	return util.RemoveAll(sessionDir)
}

// Exists checks if a session exists.
func (fs *FileStore) Exists(name string) bool {
	sessionDir := config.GetSessionDir(fs.clydeRoot, name)
	return util.DirExists(sessionDir)
}

// LoadSettings loads settings.json for a session (returns nil if not exists).
func (fs *FileStore) LoadSettings(name string) (*Settings, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, name)
	settingsPath := filepath.Join(sessionDir, settingsFile)

	if !util.FileExists(settingsPath) {
		return nil, nil
	}

	var settings Settings
	if err := util.ReadJSON(settingsPath, &settings); err != nil {
		return nil, fmt.Errorf("failed to read settings: %w", err)
	}

	return &settings, nil
}

// SaveSettings saves settings.json for a session.
func (fs *FileStore) SaveSettings(name string, settings *Settings) error {
	if err := ValidateName(name); err != nil {
		return err
	}

	if !fs.Exists(name) {
		return fmt.Errorf("session '%s' not found", name)
	}

	sessionDir := config.GetSessionDir(fs.clydeRoot, name)
	settingsPath := filepath.Join(sessionDir, settingsFile)

	return util.WriteJSON(settingsPath, settings)
}
