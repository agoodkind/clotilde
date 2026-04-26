// Synthesized Claude Code transcript JSONL for --resume continuity.
// Phase 3 of the adapter-prompt-caching plan: instead of flattening
// the whole history into a single positional prompt every turn, we
// write a JSONL transcript in the shape Claude Code expects, then
// pass --resume <session-id> so the CLI reads history from disk.
// That lets Claude's own prompt-cache / microcompact pipeline kick in
// on every turn after the first.
package fallback

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// TranscriptVersion is the value written into every line's `version`
// field. Pinned to a known-good CC release; drift is detected by the
// spawn-time version probe logged in fallback.spawn.
const TranscriptVersion = "1.0.0"

// maxSanitizedLength mirrors MAX_SANITIZED_LENGTH in
// src/utils/sessionStoragePortable.ts:293.
const maxSanitizedLength = 200

// sanitizePathRE matches Claude's sanitize: every non-alphanumeric
// character becomes `-`. sessionStoragePortable.ts:312.
var sanitizePathRE = regexp.MustCompile(`[^a-zA-Z0-9]`)

// SanitizePath mirrors sanitizePath(name) from Claude Code. Replaces
// every non-alphanumeric character with "-". For strings longer than
// maxSanitizedLength, truncates and appends a short hash for
// uniqueness, matching the upstream behavior exactly.
func SanitizePath(name string) string {
	s := sanitizePathRE.ReplaceAllString(name, "-")
	if len(s) <= maxSanitizedLength {
		return s
	}
	sum := sha1.Sum([]byte(name))
	hash := hex.EncodeToString(sum[:])[:12]
	return s[:maxSanitizedLength] + "-" + hash
}

// TranscriptPath returns the JSONL path Claude reads / writes for a
// given (claudeConfigHome, cwd, sessionID). claudeConfigHome is
// typically $CLAUDE_CONFIG_HOME or ~/.claude.
func TranscriptPath(claudeConfigHome, cwd, sessionID string) string {
	return filepath.Join(claudeConfigHome, "projects", SanitizePath(cwd), sessionID+".jsonl")
}

// TranscriptLine is one line of the JSONL transcript. Only the
// shape's required surface is modeled; unknown fields are tolerated
// by Claude's permissive parser (json.ts:146-150). Keep field order
// stable so byte-identical history lines are emitted across turns,
// which matters for Claude's upstream prompt-cache hashing.
type TranscriptLine struct {
	Type        string          `json:"type"`        // "user" | "assistant"
	UUID        string          `json:"uuid"`        // deterministic v5-style from session+index+content
	ParentUUID  *string         `json:"parentUuid"`  // nil for first line
	SessionID   string          `json:"sessionId"`   // matches --resume target
	Message     json.RawMessage `json:"message"`     // Anthropic Messages-API message shape
	Cwd         string          `json:"cwd"`         // scratch workspace dir
	UserType    string          `json:"userType"`    // always "user"
	Version     string          `json:"version"`     // CC version pin
	IsSidechain bool            `json:"isSidechain"` // false for main loop
	Timestamp   string          `json:"timestamp"`   // ISO-8601
}

// SynthesizeTranscript builds the transcript lines for the given
// message sequence. The returned slice covers all messages in order;
// callers that want to use --resume and pass only the latest user
// message as the positional prompt should slice off the final entry
// before writing.
//
// UUIDs are deterministic (sha1 of sessionID + index + role + content
// bytes, truncated and formatted as a v4-shaped string). That keeps
// the transcript byte-stable across turns so Claude's prompt cache
// sees the same leading bytes as the prior turn.
//
// System messages are dropped (Claude Code gets system via
// --append-system-prompt, not transcript lines).
func SynthesizeTranscript(msgs []Message, sessionID, cwd string, now time.Time) ([]TranscriptLine, error) {
	if sessionID == "" {
		return nil, fmt.Errorf("transcript: sessionID required")
	}
	if cwd == "" {
		return nil, fmt.Errorf("transcript: cwd required")
	}
	lines := make([]TranscriptLine, 0, len(msgs))
	var parentUUID *string
	for i, m := range msgs {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		u := deterministicUUID(sessionID, i, m.Role, m.Content)
		rawMsg, err := buildMessagePayload(m)
		if err != nil {
			return nil, fmt.Errorf("transcript: build message %d: %w", i, err)
		}
		line := TranscriptLine{
			Type:        m.Role,
			UUID:        u,
			ParentUUID:  parentUUID,
			SessionID:   sessionID,
			Message:     rawMsg,
			Cwd:         cwd,
			UserType:    "user",
			Version:     TranscriptVersion,
			IsSidechain: false,
			// Offset timestamp per-line so the file is monotonically
			// ordered but each turn has a distinct time. Claude only
			// reads this field cosmetically; the parent chain is what
			// actually orders the history.
			Timestamp: now.Add(time.Duration(i) * time.Millisecond).UTC().Format(time.RFC3339Nano),
		}
		lines = append(lines, line)
		uuidCopy := u
		parentUUID = &uuidCopy
	}
	return lines, nil
}

// WriteTranscript writes the lines to path atomically (tmp + rename).
// Truncates any existing file so each turn's transcript is fully
// regenerated from the adapter's view of history. Creates parent
// directories as needed.
func WriteTranscript(path string, lines []TranscriptLine) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("transcript: mkdir parent: %w", err)
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("transcript: create tmp: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for i, l := range lines {
		if err := enc.Encode(l); err != nil {
			f.Close()
			os.Remove(tmp)
			return fmt.Errorf("transcript: encode line %d: %w", i, err)
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("transcript: close: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("transcript: rename: %w", err)
	}
	return nil
}

// buildMessagePayload returns the Anthropic Messages-API `message`
// object for one transcript line. User messages carry `role:"user"`
// with string content. Assistant messages carry `role:"assistant"`
// with a content array of text blocks so Claude's loader recognizes
// them as proper assistant turns.
func buildMessagePayload(m Message) (json.RawMessage, error) {
	switch m.Role {
	case "user":
		payload := struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{Role: "user", Content: m.Content}
		return json.Marshal(payload)
	case "assistant":
		type textBlock struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		payload := struct {
			Role       string      `json:"role"`
			Content    []textBlock `json:"content"`
			Model      string      `json:"model"`
			StopReason string      `json:"stop_reason"`
		}{
			Role:       "assistant",
			Content:    []textBlock{{Type: "text", Text: m.Content}},
			Model:      "claude",
			StopReason: "end_turn",
		}
		return json.Marshal(payload)
	default:
		return nil, fmt.Errorf("unsupported role %q", m.Role)
	}
}

// deterministicUUID returns a UUID-shaped string derived from the
// session, turn index, role, and content bytes. Not a real RFC-4122
// v4 (we do not preserve all version / variant bits), but Claude's
// loader only cares that the string is unique and references survive
// the parent chain walk. Stability across turns (same inputs →
// same UUID) is what matters for cache hashing.
func deterministicUUID(sessionID string, index int, role, content string) string {
	h := sha1.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s", sessionID, index, role, content)
	sum := h.Sum(nil)
	hex := hex.EncodeToString(sum[:16])
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex[0:8], hex[8:12], hex[12:16], hex[16:20], hex[20:32])
}
