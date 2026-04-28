package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
)

// HitRateTracker maintains a rolling window of continuation decisions
// keyed by conversation (the prompt cache key on the wire). Each
// observation records hit or miss; the tracker returns the rolling
// hit rate and current window size on demand. The implementation is
// concurrent-safe; daemons may share one tracker across requests.
//
// Window size is fixed per conversation. Older observations beyond
// the window age out. The default window is 20 turns, matching the
// captures the slice plan calls for in Step F.5.
type HitRateTracker struct {
	mu       sync.Mutex
	window   int
	by       map[string]*hitRingBuffer
}

type hitRingBuffer struct {
	hits []bool
	next int
	full bool
}

const defaultHitRateWindow = 20

// NewHitRateTracker constructs a tracker with the given window size.
// A window <= 0 falls back to defaultHitRateWindow.
func NewHitRateTracker(window int) *HitRateTracker {
	if window <= 0 {
		window = defaultHitRateWindow
	}
	return &HitRateTracker{window: window, by: make(map[string]*hitRingBuffer)}
}

// Record appends one observation to the conversation's rolling window
// and returns the post-record hit rate plus the current window size.
// An empty conversation key is a no-op that returns (0, 0).
func (t *HitRateTracker) Record(conversationKey string, hit bool) (float64, int) {
	conversationKey = strings.TrimSpace(conversationKey)
	if t == nil || conversationKey == "" {
		return 0, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	buf, ok := t.by[conversationKey]
	if !ok {
		buf = &hitRingBuffer{hits: make([]bool, t.window)}
		t.by[conversationKey] = buf
	}
	buf.hits[buf.next] = hit
	buf.next = (buf.next + 1) % t.window
	if buf.next == 0 {
		buf.full = true
	}
	size := buf.next
	if buf.full {
		size = t.window
	}
	hits := 0
	for i := 0; i < size; i++ {
		if buf.hits[i] {
			hits++
		}
	}
	return float64(hits) / float64(size), size
}

// Forget drops the rolling window for a conversation. Called when a
// conversation key falls out of the continuation ledger so the
// tracker stops growing unbounded over long-running daemons.
func (t *HitRateTracker) Forget(conversationKey string) {
	conversationKey = strings.TrimSpace(conversationKey)
	if t == nil || conversationKey == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.by, conversationKey)
}

// MismatchDiffSummary produces a stable short hash of the expected
// and current mismatch payload strings. The hash is stable across
// runs so log queries can group equivalent mismatches without
// retaining the full payload. Returns an empty string when both
// inputs are empty.
func MismatchDiffSummary(expected, current string) string {
	expected = strings.TrimSpace(expected)
	current = strings.TrimSpace(current)
	if expected == "" && current == "" {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(expected))
	h.Write([]byte{0})
	h.Write([]byte(current))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:6])
}
