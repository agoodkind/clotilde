package session

import (
	"fmt"
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
}

// NewFileStore creates a new FileStore.
func NewFileStore(clydeRoot string) *FileStore {
	return &FileStore{
		clydeRoot: clydeRoot,
	}
}

// NewGlobalFileStore creates a FileStore pointing at the global sessions directory.
// Creates the directory if it doesn't exist.
func NewGlobalFileStore() (*FileStore, error) {
	if err := config.EnsureGlobalSessionsDir(); err != nil {
		return nil, fmt.Errorf("failed to create global sessions directory: %w", err)
	}
	return &FileStore{clydeRoot: config.GlobalDataDir()}, nil
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
	// Tier 1: exact name match
	if sess, err := fs.Get(query); err == nil {
		return sess, nil
	}

	// Tier 2: UUID match
	if _, parseErr := uuid.Parse(query); parseErr == nil {
		sessions, listErr := fs.List()
		if listErr == nil {
			for _, sess := range sessions {
				if sess.Metadata.SessionID == query {
					return sess, nil
				}
				if slices.Contains(sess.Metadata.PreviousSessionIDs, query) {
					return sess, nil
				}
			}
		}
	}

	// Tier 3: substring search (single match only)
	matches, err := fs.Search(query)
	if err != nil || len(matches) == 0 {
		return nil, nil
	}
	if len(matches) == 1 {
		return matches[0], nil
	}

	// Multiple matches: return nil so caller can show picker or error
	return nil, nil
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
