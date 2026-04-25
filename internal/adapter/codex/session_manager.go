package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

const (
	defaultSessionTTL = 20 * time.Minute
	defaultSessionMax = 8
)

type ManagedPromptPlan struct {
	System            string
	FullPrompt        string
	IncrementalPrompt string
	AssistantAnchor   string
}

func NormalizeAssistantAnchor(text string, sanitize func(string) string) string {
	return strings.TrimSpace(sanitize(text))
}

func DeriveCacheCreationTokens(previousCachedInputTokens, currentCachedInputTokens int) int {
	derived := currentCachedInputTokens - previousCachedInputTokens
	if derived < 0 {
		return 0
	}
	return derived
}

func BuildManagedPromptPlan(
	messages []adapteropenai.ChatMessage,
	buildPrompt func([]adapteropenai.ChatMessage) (string, string),
	flatten func(json.RawMessage) string,
	sanitize func(string) string,
) ManagedPromptPlan {
	system, fullPrompt := buildPrompt(messages)
	lastAssistant := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if strings.EqualFold(strings.TrimSpace(messages[i].Role), "assistant") {
			lastAssistant = i
			break
		}
	}
	incrementalMsgs := messages
	assistantAnchor := ""
	if lastAssistant >= 0 {
		incrementalMsgs = messages[lastAssistant+1:]
		assistantAnchor = NormalizeAssistantAnchor(flatten(messages[lastAssistant].Content), sanitize)
	}
	_, incrementalPrompt := buildPrompt(incrementalMsgs)
	incrementalPrompt = strings.TrimSpace(incrementalPrompt)
	if incrementalPrompt == "" {
		incrementalPrompt = strings.TrimSpace(fullPrompt)
	}
	return ManagedPromptPlan{
		System:            system,
		FullPrompt:        strings.TrimSpace(fullPrompt),
		IncrementalPrompt: incrementalPrompt,
		AssistantAnchor:   assistantAnchor,
	}
}

type SessionSpec struct {
	Key     string
	Model   string
	Effort  string
	Summary string
	System  string
}

type SessionAcquireResult struct {
	Session     *ManagedSession
	Created     bool
	ResetReason string
}

type ManagedTransport interface {
	Close() error
}

type ManagedSession struct {
	Key           string
	Transport     ManagedTransport
	Model         string
	Effort        string
	Summary       string
	System        string
	LastAssistant string
	CreatedAt     time.Time
	LastUsed      time.Time
	RunMu         sync.Mutex
	Refs          int
}

type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*ManagedSession
	ttl      time.Duration
	max      int
	log      *slog.Logger
	now      func() time.Time
	start    func(spec SessionSpec) (ManagedTransport, error)
}

func NewSessionManager(log *slog.Logger, start func(spec SessionSpec) (ManagedTransport, error)) *SessionManager {
	if log == nil {
		log = slog.Default()
	}
	return &SessionManager{
		sessions: make(map[string]*ManagedSession),
		ttl:      defaultSessionTTL,
		max:      defaultSessionMax,
		log:      log,
		now:      time.Now,
		start:    start,
	}
}

func (m *SessionManager) Acquire(ctx context.Context, spec SessionSpec) (SessionAcquireResult, error) {
	if m == nil {
		return SessionAcquireResult{}, fmt.Errorf("codex session manager not configured")
	}
	now := m.now()
	var toClose []closeWithReason

	m.mu.Lock()
	toClose = append(toClose, m.sweepLocked(now)...)
	if existing := m.sessions[spec.Key]; existing != nil {
		if reason := sessionResetReason(existing, spec); reason != "" {
			delete(m.sessions, spec.Key)
			toClose = append(toClose, closeWithReason{session: existing, reason: reason})
		} else {
			existing.Refs++
			existing.LastUsed = now
			m.mu.Unlock()
			closeSessions(toClose)
			return SessionAcquireResult{Session: existing}, nil
		}
	}
	m.mu.Unlock()
	closeSessions(toClose)

	transport, err := m.start(spec)
	if err != nil {
		return SessionAcquireResult{}, err
	}
	session := &ManagedSession{
		Key:       spec.Key,
		Transport: transport,
		Model:     spec.Model,
		Effort:    spec.Effort,
		Summary:   spec.Summary,
		System:    spec.System,
		CreatedAt: now,
		LastUsed:  now,
		Refs:      1,
	}

	var postCreateClose []closeWithReason
	m.mu.Lock()
	postCreateClose = append(postCreateClose, m.sweepLocked(now)...)
	if existing := m.sessions[spec.Key]; existing != nil {
		if reason := sessionResetReason(existing, spec); reason == "" {
			existing.Refs++
			existing.LastUsed = now
			m.mu.Unlock()
			_ = transport.Close()
			closeSessions(postCreateClose)
			return SessionAcquireResult{Session: existing}, nil
		}
		delete(m.sessions, spec.Key)
		postCreateClose = append(postCreateClose, closeWithReason{session: existing, reason: "replaced_during_create"})
	}
	m.sessions[spec.Key] = session
	postCreateClose = append(postCreateClose, m.enforceCapLocked(now)...)
	m.mu.Unlock()
	closeSessions(postCreateClose)
	return SessionAcquireResult{Session: session, Created: true}, nil
}

func (m *SessionManager) Release(session *ManagedSession) {
	if m == nil || session == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if current := m.sessions[session.Key]; current == session && session.Refs > 0 {
		session.Refs--
		session.LastUsed = m.now()
	}
}

func (m *SessionManager) Drop(session *ManagedSession, reason string) {
	if m == nil || session == nil {
		return
	}
	m.mu.Lock()
	if current := m.sessions[session.Key]; current == session {
		delete(m.sessions, session.Key)
	}
	m.mu.Unlock()
	_ = session.Transport.Close()
	m.log.LogAttrs(context.Background(), slog.LevelInfo, "adapter.codex.session.dropped",
		slog.String("session_key", session.Key),
		slog.String("reason", reason),
	)
}

func (m *SessionManager) CloseAll() {
	if m == nil {
		return
	}
	m.mu.Lock()
	sessions := make([]*ManagedSession, 0, len(m.sessions))
	for key, session := range m.sessions {
		delete(m.sessions, key)
		sessions = append(sessions, session)
	}
	m.mu.Unlock()
	for _, session := range sessions {
		_ = session.Transport.Close()
	}
}

type closeWithReason struct {
	session *ManagedSession
	reason  string
}

func closeSessions(items []closeWithReason) {
	for _, item := range items {
		if item.session == nil {
			continue
		}
		_ = item.session.Transport.Close()
	}
}

func sessionResetReason(session *ManagedSession, spec SessionSpec) string {
	switch {
	case session.Model != spec.Model:
		return "model_changed"
	case session.Effort != spec.Effort:
		return "effort_changed"
	case session.Summary != spec.Summary:
		return "summary_changed"
	case session.System != spec.System:
		return "system_changed"
	default:
		return ""
	}
}

func (m *SessionManager) sweepLocked(now time.Time) []closeWithReason {
	if m.ttl <= 0 {
		return nil
	}
	var out []closeWithReason
	for key, session := range m.sessions {
		if session.Refs > 0 {
			continue
		}
		if now.Sub(session.LastUsed) <= m.ttl {
			continue
		}
		delete(m.sessions, key)
		out = append(out, closeWithReason{session: session, reason: "idle_ttl"})
	}
	return out
}

func (m *SessionManager) enforceCapLocked(now time.Time) []closeWithReason {
	if m.max <= 0 || len(m.sessions) <= m.max {
		return nil
	}
	var out []closeWithReason
	for len(m.sessions) > m.max {
		var oldestKey string
		var oldest *ManagedSession
		for key, session := range m.sessions {
			if session.Refs > 0 {
				continue
			}
			if oldest == nil || session.LastUsed.Before(oldest.LastUsed) {
				oldest = session
				oldestKey = key
			}
		}
		if oldest == nil {
			break
		}
		delete(m.sessions, oldestKey)
		out = append(out, closeWithReason{session: oldest, reason: "max_sessions"})
	}
	_ = now
	return out
}
