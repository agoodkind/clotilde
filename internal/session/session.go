package session

import (
	"slices"
	"time"
)

// Session represents a named Claude Code session.
type Session struct {
	Name     string
	Metadata Metadata
}

// Metadata represents the session metadata stored in metadata.json.
type Metadata struct {
	Name                 string    `json:"name"`
	SessionID            string    `json:"sessionId"`
	TranscriptPath       string    `json:"transcriptPath,omitempty"`
	WorkDir              string    `json:"workDir,omitempty"`
	Created              time.Time `json:"created"`
	LastAccessed         time.Time `json:"lastAccessed"`
	ParentSession        string    `json:"parentSession,omitempty"`
	IsForkedSession      bool      `json:"isForkedSession"`
	IsIncognito          bool      `json:"isIncognito"`
	PreviousSessionIDs   []string  `json:"previousSessionIds,omitempty"`
	Context              string    `json:"context,omitempty"`
	HasCustomOutputStyle bool      `json:"hasCustomOutputStyle,omitempty"`
	WorkspaceRoot        string    `json:"workspaceRoot,omitempty"`
	DisplayName          string    `json:"displayName,omitempty"`
}

// Settings represents Claude Code session-specific settings stored in settings.json.
type Settings struct {
	Model       string      `json:"model,omitempty"`
	EffortLevel string      `json:"effortLevel,omitempty"`
	OutputStyle string      `json:"outputStyle,omitempty"`
	Permissions Permissions `json:"permissions,omitzero"`
}

// Permissions represents the permissions configuration for a session.
type Permissions struct {
	Allow                        []string `json:"allow,omitempty"`
	Ask                          []string `json:"ask,omitempty"`
	Deny                         []string `json:"deny,omitempty"`
	AdditionalDirectories        []string `json:"additionalDirectories,omitempty"`
	DefaultMode                  string   `json:"defaultMode,omitempty"`
	DisableBypassPermissionsMode string   `json:"disableBypassPermissionsMode,omitempty"`
}

// NewSession creates a new session with the given name and UUID.
func NewSession(name, sessionID string) *Session {
	now := time.Now()
	return &Session{
		Name: name,
		Metadata: Metadata{
			Name:            name,
			SessionID:       sessionID,
			Created:         now,
			LastAccessed:    now,
			IsForkedSession: false,
		},
	}
}

// NewIncognitoSession creates a new incognito session that will auto-delete on exit.
func NewIncognitoSession(name, sessionID string) *Session {
	sess := NewSession(name, sessionID)
	sess.Metadata.IsIncognito = true
	return sess
}

// UpdateLastAccessed updates the lastAccessed timestamp to now.
func (s *Session) UpdateLastAccessed() {
	s.Metadata.LastAccessed = time.Now()
}

// DisplayName returns the human-readable display name for the session.
// If a DisplayName has been set (e.g. via auto-name), it is returned.
// Otherwise the raw session Name (e.g. configs-6d383f1d) is returned.
func (s *Session) DisplayName() string {
	if s.Metadata.DisplayName != "" {
		return s.Metadata.DisplayName
	}
	return s.Name
}

// AddPreviousSessionID appends the current session ID to the history and updates to the new ID.
// This is idempotent - won't add duplicates.
func (s *Session) AddPreviousSessionID(newSessionID string) {
	// Only add current ID to history if it's not empty and different from new ID
	if s.Metadata.SessionID != "" && s.Metadata.SessionID != newSessionID {
		if !slices.Contains(s.Metadata.PreviousSessionIDs, s.Metadata.SessionID) {
			s.Metadata.PreviousSessionIDs = append(s.Metadata.PreviousSessionIDs, s.Metadata.SessionID)
		}
	}

	s.Metadata.SessionID = newSessionID
}
