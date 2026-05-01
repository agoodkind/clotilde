package session

import "time"

// Session represents a named provider session.
type Session struct {
	Name     string
	Metadata Metadata
}

// Metadata represents the session metadata stored in metadata.json.
type Metadata struct {
	Name                 string     `json:"name"`
	Provider             ProviderID `json:"provider,omitempty"`
	SessionID            string     `json:"sessionId"`
	TranscriptPath       string     `json:"transcriptPath,omitempty"`
	WorkDir              string     `json:"workDir,omitempty"`
	Created              time.Time  `json:"created"`
	LastAccessed         time.Time  `json:"lastAccessed"`
	ParentSession        string     `json:"parentSession,omitempty"`
	IsForkedSession      bool       `json:"isForkedSession"`
	IsIncognito          bool       `json:"isIncognito"`
	PreviousSessionIDs   []string   `json:"previousSessionIds,omitempty"`
	Context              string     `json:"context,omitempty"`
	HasCustomOutputStyle bool       `json:"hasCustomOutputStyle,omitempty"`
	WorkspaceRoot        string     `json:"workspaceRoot,omitempty"`

	// ContextMessageCount is the message count at the moment Context was
	// last generated. The TUI uses it to decide when the summary is stale
	// and should be regenerated in the background.
	ContextMessageCount int `json:"contextMessageCount,omitempty"`

	// DisplayTitle preserves the original user-given chat name that
	// Claude Code stores in transcript "custom-title" entries. It is the
	// human-readable form surfaced in the TUI. The session Name is a
	// sanitized derivative used as the directory identifier and is what
	// clyde resume, compact, and other verbs accept. DisplayTitle stays
	// in sync with the latest custom-title entry seen during scan; the
	// Name never renames post-adoption because that would break
	// previousSessionIds and parentSession references.
	DisplayTitle string `json:"displayTitle,omitempty"`
}

// Settings represents Claude Code session-specific settings stored in settings.json.
type Settings struct {
	Model         string      `json:"model,omitempty"`
	EffortLevel   string      `json:"effortLevel,omitempty"`
	OutputStyle   string      `json:"outputStyle,omitempty"`
	RemoteControl bool        `json:"remoteControl,omitempty"`
	Permissions   Permissions `json:"permissions,omitzero"`
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
			Provider:        ProviderClaude,
			SessionID:       sessionID,
			Created:         now,
			LastAccessed:    now,
			IsForkedSession: false,
		},
	}
}

// UpdateLastAccessed updates the lastAccessed timestamp to now.
func (s *Session) UpdateLastAccessed() {
	s.Metadata.LastAccessed = time.Now()
}

// ProviderID returns the normalized provider for the session.
func (s *Session) ProviderID() ProviderID {
	return s.Metadata.ProviderID()
}

// Identity returns the provider-aware identity for the session.
func (s *Session) Identity() SessionIdentity {
	return s.Metadata.Identity(s.Name)
}

// SessionProviderCapabilities returns the capabilities for the session provider.
func (s *Session) SessionProviderCapabilities() ProviderCapabilities {
	return ProviderInfo(s.ProviderID()).Capabilities
}

// ProviderID returns the normalized provider for stored metadata.
func (m Metadata) ProviderID() ProviderID {
	return NormalizeProviderID(m.Provider)
}

// Identity returns the provider-aware identity for stored metadata.
func (m Metadata) Identity(name string) SessionIdentity {
	previous := make([]ProviderSessionID, 0, len(m.PreviousSessionIDs))
	provider := m.ProviderID()
	for _, previousSessionID := range m.PreviousSessionIDs {
		previous = append(previous, ProviderSessionID{
			Provider: provider,
			ID:       previousSessionID,
		})
	}
	return SessionIdentity{
		Name: name,
		Current: ProviderSessionID{
			Provider: provider,
			ID:       m.SessionID,
		},
		Previous: previous,
	}
}

// RotateIdentity records the current provider session id as historical state and
// replaces it with the new primary identity.
func (s *Session) RotateIdentity(next ProviderSessionID) {
	identity := s.Identity()
	next = next.Normalized()
	if current := identity.Current.Normalized(); !current.IsZero() && current != next {
		s.Metadata.PreviousSessionIDs = appendUniqueString(s.Metadata.PreviousSessionIDs, current.ID)
	}
	s.Metadata.Provider = next.Provider
	s.Metadata.SessionID = next.ID
}

// AddPreviousSessionID appends the current session ID to the history and updates to the new ID.
// This is idempotent - won't add duplicates.
func (s *Session) AddPreviousSessionID(newSessionID string) {
	s.RotateIdentity(ProviderSessionID{
		Provider: s.ProviderID(),
		ID:       newSessionID,
	})
}
