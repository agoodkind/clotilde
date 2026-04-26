// Deterministic session-id derivation. Same conversation yields the
// same UUID across back-to-back fallback invocations so `claude -p`
// writes to a stable transcript file, which in turn stabilizes the
// byte sequence the upstream prompt cache hashes against.
package fallback

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// DeriveSessionID builds a UUIDv4-shaped string from the hash of the
// first user message and the model alias. Two requests from the same
// Cursor conversation hash to the same UUID because Cursor resends the
// full history on every turn and the first user message never changes.
// An empty first message (defensive) yields "" so the caller skips
// passing --session-id; Claude Code then allocates its own.
func DeriveSessionID(firstUserMessage, modelAlias string) string {
	trimmed := strings.TrimSpace(firstUserMessage)
	if trimmed == "" {
		return ""
	}
	h := sha256.New()
	_, _ = h.Write([]byte(modelAlias))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(trimmed))
	sum := h.Sum(nil)
	// Set UUIDv4 variant + version bits so `--session-id` parsers that
	// validate the shape still accept it.
	sum[6] = (sum[6] & 0x0f) | 0x40
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// firstUserMessage returns the text of the earliest user-role message
// in msgs, or "" if none is present. Matches the heuristic callers
// feed into DeriveSessionID.
func firstUserMessage(msgs []Message) string {
	for _, m := range msgs {
		if strings.EqualFold(strings.TrimSpace(m.Role), "user") {
			return m.Content
		}
	}
	return ""
}
