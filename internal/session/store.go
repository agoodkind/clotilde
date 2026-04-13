package session

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/util"
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

	// GetByDisplayName searches all sessions for one whose DisplayName matches.
	// Returns nil if no match is found.
	GetByDisplayName(displayName string) (*Session, error)

	// Search returns sessions matching query against name, display name, UUID,
	// and context (case-insensitive substring match).
	Search(query string) ([]*Session, error)

	// Create creates a new session folder structure with metadata
	Create(session *Session) error

	// Update updates session metadata
	Update(session *Session) error

	// Delete removes a session folder and all its contents
	Delete(name string) error

	// Exists checks if a session exists
	Exists(name string) bool

	// LoadSettings loads settings.json for a session (returns nil if not exists)
	LoadSettings(name string) (*Settings, error)

	// SaveSettings saves settings.json for a session
	SaveSettings(name string, settings *Settings) error
}

// FileStore implements Store using the filesystem.
type FileStore struct {
	clotildeRoot string
}

// NewFileStore creates a new FileStore.
func NewFileStore(clotildeRoot string) *FileStore {
	return &FileStore{
		clotildeRoot: clotildeRoot,
	}
}

// NewGlobalFileStore creates a FileStore pointing at the global sessions directory.
// Creates the directory if it doesn't exist.
func NewGlobalFileStore() (*FileStore, error) {
	if err := config.EnsureGlobalSessionsDir(); err != nil {
		return nil, fmt.Errorf("failed to create global sessions directory: %w", err)
	}
	return &FileStore{clotildeRoot: config.GlobalDataDir()}, nil
}

// List returns all sessions, sorted by lastAccessed (most recent first).
func (fs *FileStore) List() ([]*Session, error) {
	sessionsDir := config.GetSessionsDir(fs.clotildeRoot)

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

	sessionDir := config.GetSessionDir(fs.clotildeRoot, name)
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

// Search returns sessions matching query against name, display name, UUID,
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
			strings.Contains(strings.ToLower(sess.Metadata.DisplayName), q) ||
			strings.Contains(strings.ToLower(sess.Metadata.SessionID), q) ||
			strings.Contains(strings.ToLower(sess.Metadata.Context), q) {
			matches = append(matches, sess)
		}
	}
	return matches, nil
}

// GetByDisplayName searches all sessions for one whose DisplayName matches.
func (fs *FileStore) GetByDisplayName(displayName string) (*Session, error) {
	sessions, err := fs.List()
	if err != nil {
		return nil, err
	}
	for _, sess := range sessions {
		if sess.Metadata.DisplayName == displayName {
			return sess, nil
		}
	}
	return nil, fmt.Errorf("no session found with display name %q", displayName)
}

// Create creates a new session folder structure with metadata.
func (fs *FileStore) Create(session *Session) error {
	if err := ValidateName(session.Name); err != nil {
		return err
	}

	if fs.Exists(session.Name) {
		return fmt.Errorf("session '%s' already exists", session.Name)
	}

	sessionDir := config.GetSessionDir(fs.clotildeRoot, session.Name)
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

	sessionDir := config.GetSessionDir(fs.clotildeRoot, session.Name)
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

	sessionDir := config.GetSessionDir(fs.clotildeRoot, name)
	return util.RemoveAll(sessionDir)
}

// Exists checks if a session exists.
func (fs *FileStore) Exists(name string) bool {
	sessionDir := config.GetSessionDir(fs.clotildeRoot, name)
	return util.DirExists(sessionDir)
}

// LoadSettings loads settings.json for a session (returns nil if not exists).
func (fs *FileStore) LoadSettings(name string) (*Settings, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}

	sessionDir := config.GetSessionDir(fs.clotildeRoot, name)
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

	sessionDir := config.GetSessionDir(fs.clotildeRoot, name)
	settingsPath := filepath.Join(sessionDir, settingsFile)

	return util.WriteJSON(settingsPath, settings)
}
