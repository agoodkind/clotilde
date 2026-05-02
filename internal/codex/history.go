package codex

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// ThreadSource is the typed subset of Codex SessionSource that Clyde needs for
// local discovery and details rendering.
type ThreadSource struct {
	Kind           string
	ParentThreadID string
}

// ThreadSummary is a provider-owned summary of a Codex rollout thread.
type ThreadSummary struct {
	ID            string
	RolloutPath   string
	ForkedFromID  string
	Preview       string
	Name          string
	ModelProvider string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	CWD           string
	CLIVersion    string
	Originator    string
	Source        ThreadSource
	AgentNickname string
	AgentRole     string
	IsSubagent    bool
	IsArchived    bool
	Messages      []HistoryMessage
}

// HistoryMessage is a normalized conversational message extracted from Codex
// rollout entries.
type HistoryMessage struct {
	Role      string
	Text      string
	Timestamp time.Time
	Phase     string
}

// ReadThreadByRolloutPath returns a Codex rollout thread summary by JSONL path.
func ReadThreadByRolloutPath(path string, includeHistory bool, archived bool) (ThreadSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return ThreadSummary{}, err
	}
	defer func() { _ = f.Close() }()

	summary := ThreadSummary{
		RolloutPath: path,
		IsArchived:  archived,
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var envelope historyLine
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}
		lineTime := parseCodexTime(envelope.Timestamp)
		if !lineTime.IsZero() {
			summary.UpdatedAt = lineTime
		}
		switch envelope.Type {
		case "session_meta":
			var payload sessionMetaPayload
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				return ThreadSummary{}, err
			}
			applySessionMeta(&summary, payload, lineTime)
		case "response_item":
			msg, ok := responseItemMessage(envelope.Payload, lineTime)
			if ok {
				applyMessage(&summary, msg, includeHistory)
			}
		case "event_msg":
			msg, ok := eventMessage(envelope.Payload, lineTime)
			if ok {
				applyMessage(&summary, msg, includeHistory)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return ThreadSummary{}, err
	}
	if summary.ID == "" {
		return ThreadSummary{}, fmt.Errorf("codex rollout %s missing session_meta id", path)
	}
	if summary.UpdatedAt.IsZero() {
		if stat, err := os.Stat(path); err == nil {
			summary.UpdatedAt = stat.ModTime()
		}
	}
	return summary, nil
}

type historyLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type sessionMetaPayload struct {
	ID            string      `json:"id"`
	Timestamp     string      `json:"timestamp"`
	CWD           string      `json:"cwd"`
	Originator    string      `json:"originator"`
	CLIVersion    string      `json:"cli_version"`
	ModelProvider string      `json:"model_provider"`
	Source        sourceUnion `json:"source"`
	AgentNickname string      `json:"agent_nickname"`
	AgentRole     string      `json:"agent_role"`
}

// sourceUnion models the SessionSource union from research/codex and keeps the
// raw union localized at the file-format boundary.
type sourceUnion struct {
	ThreadSource
}

func (s *sourceUnion) UnmarshalJSON(data []byte) error {
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		s.Kind = scalar
		return nil
	}
	var object sourceObject
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	switch {
	case object.Subagent.ThreadSpawn.ParentThreadID != "":
		s.Kind = "subAgent"
		s.ParentThreadID = object.Subagent.ThreadSpawn.ParentThreadID
	case object.Custom != "":
		s.Kind = "custom"
	default:
		s.Kind = "unknown"
	}
	return nil
}

type sourceObject struct {
	Custom   string `json:"custom"`
	Subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

type responsePayload struct {
	Type    string        `json:"type"`
	Role    string        `json:"role"`
	Content []contentPart `json:"content"`
	Phase   string        `json:"phase"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type eventPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Phase   string `json:"phase"`
}

func applySessionMeta(summary *ThreadSummary, payload sessionMetaPayload, lineTime time.Time) {
	summary.ID = payload.ID
	summary.CWD = payload.CWD
	summary.Originator = payload.Originator
	summary.CLIVersion = payload.CLIVersion
	summary.ModelProvider = payload.ModelProvider
	summary.Source = payload.Source.ThreadSource
	summary.AgentNickname = payload.AgentNickname
	summary.AgentRole = payload.AgentRole
	summary.ForkedFromID = payload.Source.ParentThreadID
	summary.IsSubagent = payload.Source.ParentThreadID != "" || payload.AgentNickname != "" || payload.AgentRole != ""
	if created := parseCodexTime(payload.Timestamp); !created.IsZero() {
		summary.CreatedAt = created
	} else if !lineTime.IsZero() {
		summary.CreatedAt = lineTime
	}
	if summary.UpdatedAt.IsZero() {
		summary.UpdatedAt = summary.CreatedAt
	}
}

func applyMessage(summary *ThreadSummary, msg HistoryMessage, includeHistory bool) {
	if summary.Preview == "" && msg.Role == "user" {
		summary.Preview = msg.Text
	}
	if includeHistory {
		summary.Messages = append(summary.Messages, msg)
	}
}

func responseItemMessage(raw json.RawMessage, timestamp time.Time) (HistoryMessage, bool) {
	var payload responsePayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return HistoryMessage{}, false
	}
	if payload.Type != "message" {
		return HistoryMessage{}, false
	}
	text := strings.TrimSpace(contentText(payload.Content))
	if text == "" {
		return HistoryMessage{}, false
	}
	return HistoryMessage{
		Role:      payload.Role,
		Text:      text,
		Timestamp: timestamp,
		Phase:     payload.Phase,
	}, true
}

func eventMessage(raw json.RawMessage, timestamp time.Time) (HistoryMessage, bool) {
	var payload eventPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return HistoryMessage{}, false
	}
	var role string
	switch payload.Type {
	case "user_message":
		role = "user"
	case "agent_message":
		role = "assistant"
	default:
		return HistoryMessage{}, false
	}
	text := strings.TrimSpace(payload.Message)
	if text == "" {
		return HistoryMessage{}, false
	}
	return HistoryMessage{
		Role:      role,
		Text:      text,
		Timestamp: timestamp,
		Phase:     payload.Phase,
	}, true
}

func contentText(parts []contentPart) string {
	var b strings.Builder
	for _, part := range parts {
		switch part.Type {
		case "input_text", "output_text":
			if part.Text == "" {
				continue
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func parseCodexTime(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, value); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t
	}
	return time.Time{}
}
