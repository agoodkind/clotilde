package codex

import (
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WebsocketSession holds the state codex CLI and Codex Desktop both
// hold per conversation. Reference: research/codex/codex-rs/core/src/
// client.rs::WebsocketSession. We keep the live websocket connection,
// the most recent server-issued response_id (for chaining via
// previous_response_id), and a baseline of input items so the next
// turn can compute a delta.
type WebsocketSession struct {
	Conn               *websocket.Conn
	ConversationID     string
	LastResponseID     string
	LastInputItems     []map[string]any
	OpenedAt           time.Time
	LastUsed           time.Time
	FrameCount         int
	Closed             bool
	InvalidationReason string
}

// WebsocketSessionCache holds at most one WebsocketSession per
// conversation_id. Take/Put serializes concurrent requests on the
// same conversation. The lifecycle contract:
//
// - Take removes the entry from the cache and returns it. A second
//   Take before Put returns (nil, false) so concurrent callers do
//   not share a single websocket frame stream.
// - Put returns the entry to the cache. LastUsed is bumped to now.
// - Invalidate closes the connection synchronously and drops the
//   entry. Subsequent Take returns (nil, false).
// - Idle expiry: Take returns (nil, false) and closes the entry if
//   LastUsed plus idleTTL is in the past.
// - CloseAll closes every entry. Called on daemon shutdown.
type WebsocketSessionCache struct {
	mu      sync.Mutex
	entries map[string]*WebsocketSession
	log     *slog.Logger
	idleTTL time.Duration
	now     func() time.Time
}

// NewWebsocketSessionCache constructs a cache with the given idle
// TTL. The log argument is used by Invalidate and CloseAll for
// per-action telemetry.
func NewWebsocketSessionCache(log *slog.Logger, idleTTL time.Duration) *WebsocketSessionCache {
	return &WebsocketSessionCache{
		entries: map[string]*WebsocketSession{},
		log:     log,
		idleTTL: idleTTL,
		now:     time.Now,
	}
}

// Take removes the entry for conversationID and returns it. If the
// entry is missing, expired, or closed, returns (nil, false). Stale
// entries are closed before returning false.
func (c *WebsocketSessionCache) Take(conversationID string) (*WebsocketSession, bool) {
	conv := strings.TrimSpace(conversationID)
	if conv == "" {
		return nil, false
	}
	c.mu.Lock()
	entry, ok := c.entries[conv]
	if !ok {
		c.mu.Unlock()
		return nil, false
	}
	delete(c.entries, conv)
	c.mu.Unlock()
	if entry.Closed {
		return nil, false
	}
	if c.idleTTL > 0 && c.now().Sub(entry.LastUsed) > c.idleTTL {
		c.invalidateEntry(entry, "idle_timeout")
		return nil, false
	}
	return entry, true
}

// Put returns the entry to the cache. Subsequent Take with the
// matching conversationID will retrieve it. Calls Invalidate when
// the entry is already marked closed.
func (c *WebsocketSessionCache) Put(s *WebsocketSession) {
	if s == nil {
		return
	}
	conv := strings.TrimSpace(s.ConversationID)
	if conv == "" {
		c.invalidateEntry(s, "missing_conversation_id")
		return
	}
	if s.Closed {
		c.invalidateEntry(s, "already_closed_on_put")
		return
	}
	s.LastUsed = c.now()
	c.mu.Lock()
	if existing, ok := c.entries[conv]; ok && existing != s {
		c.mu.Unlock()
		c.invalidateEntry(existing, "displaced_by_put")
		c.mu.Lock()
	}
	c.entries[conv] = s
	c.mu.Unlock()
}

// Invalidate closes any cached entry for conversationID. The reason
// is recorded on the entry and emitted via telemetry. Safe to call
// when the entry is absent.
func (c *WebsocketSessionCache) Invalidate(conversationID, reason string) {
	conv := strings.TrimSpace(conversationID)
	if conv == "" {
		return
	}
	c.mu.Lock()
	entry, ok := c.entries[conv]
	if ok {
		delete(c.entries, conv)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	c.invalidateEntry(entry, reason)
}

// CloseAll closes every cached entry. Daemon shutdown calls this so
// no websocket leaks across reload boundaries.
func (c *WebsocketSessionCache) CloseAll(reason string) {
	c.mu.Lock()
	entries := c.entries
	c.entries = map[string]*WebsocketSession{}
	c.mu.Unlock()
	for _, entry := range entries {
		c.invalidateEntry(entry, reason)
	}
}

// Size returns the number of cached entries. Used by telemetry to
// surface cache depth on each ws_session log event.
func (c *WebsocketSessionCache) Size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func (c *WebsocketSessionCache) invalidateEntry(entry *WebsocketSession, reason string) {
	if entry == nil {
		return
	}
	entry.Closed = true
	entry.InvalidationReason = reason
	if entry.Conn != nil {
		_ = entry.Conn.Close()
	}
	if c.log != nil {
		c.log.Info("adapter.codex.ws_session.invalidated",
			"component", "adapter",
			"subcomponent", "codex",
			"conversation_id", entry.ConversationID,
			"reason", reason,
			"frame_count", entry.FrameCount,
			"last_response_id", entry.LastResponseID,
			"age_ms", c.now().Sub(entry.OpenedAt).Milliseconds(),
		)
	}
}
