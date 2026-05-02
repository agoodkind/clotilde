package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type codexDiscoveryScanner struct {
	codexHome string
}

func newCodexDiscoveryScanner(codexHome string) DiscoveryScanner {
	return codexDiscoveryScanner{codexHome: codexHome}
}

func (s codexDiscoveryScanner) Provider() ProviderID {
	return ProviderCodex
}

func (s codexDiscoveryScanner) Scan() ([]DiscoveryResult, error) {
	started := currentTime()
	roots := []string{
		filepath.Join(s.codexHome, "sessions"),
	}
	var out []DiscoveryResult
	for _, root := range roots {
		scanned, err := s.scanRoot(root)
		if err != nil {
			return nil, err
		}
		out = append(out, scanned...)
	}
	sessionScanLog.Logger().Debug("session.scan.completed",
		"component", "session",
		"subcomponent", "scan",
		"provider", s.Provider(),
		"codex_home", s.codexHome,
		"transcripts", len(out),
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return out, nil
}

func (s codexDiscoveryScanner) scanRoot(root string) ([]DiscoveryResult, error) {
	var out []DiscoveryResult
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) || os.IsPermission(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		dr, ok := readCodexRolloutHeader(path)
		if !ok {
			return nil
		}
		out = append(out, dr)
		return nil
	})
	if err != nil {
		sessionScanLog.Logger().Warn("session.scan.walk_failed",
			"component", "session",
			"subcomponent", "scan",
			"provider", s.Provider(),
			"root", root,
			"err", err,
		)
		return nil, err
	}
	return out, nil
}

type codexRolloutLine struct {
	Timestamp string          `json:"timestamp"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
}

type codexSessionMetaPayload struct {
	ID            string             `json:"id"`
	Timestamp     string             `json:"timestamp"`
	CWD           string             `json:"cwd"`
	Originator    string             `json:"originator"`
	CLIVersion    string             `json:"cli_version"`
	ModelProvider string             `json:"model_provider"`
	Source        codexSessionSource `json:"source"`
	AgentNickname string             `json:"agent_nickname"`
	AgentRole     string             `json:"agent_role"`
}

// codexSessionSource models the SessionSource union observed in
// research/codex/codex-rs/app-server-protocol/schema/typescript/v2/SessionSource.ts.
type codexSessionSource struct {
	Kind           string
	ParentThreadID string
}

func (s *codexSessionSource) UnmarshalJSON(data []byte) error {
	var scalar string
	if err := json.Unmarshal(data, &scalar); err == nil {
		s.Kind = scalar
		return nil
	}
	var object codexSessionSourceObject
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

type codexSessionSourceObject struct {
	Custom   string `json:"custom"`
	Subagent struct {
		ThreadSpawn struct {
			ParentThreadID string `json:"parent_thread_id"`
		} `json:"thread_spawn"`
	} `json:"subagent"`
}

func readCodexRolloutHeader(path string) (DiscoveryResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveryResult{}, false
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var envelope codexRolloutLine
		if err := json.Unmarshal(line, &envelope); err != nil {
			continue
		}
		if envelope.Type != "session_meta" {
			continue
		}
		var payload codexSessionMetaPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			return DiscoveryResult{}, false
		}
		if payload.ID == "" {
			return DiscoveryResult{}, false
		}
		createdAt := payload.Timestamp
		if createdAt == "" {
			createdAt = envelope.Timestamp
		}
		var firstEntryTime time.Time
		if createdAt != "" {
			firstEntryTime, _ = time.Parse(time.RFC3339Nano, createdAt)
		}
		dr := DiscoveryResult{
			Provider: ProviderCodex,
			Identity: ProviderSessionID{
				Provider: ProviderCodex,
				ID:       payload.ID,
			},
			WorkspaceRoot:  payload.CWD,
			Entrypoint:     payload.Originator,
			FirstEntryTime: firstEntryTime,
			IsSubagent:     payload.Source.ParentThreadID != "" || payload.AgentNickname != "" || payload.AgentRole != "",
			Claude: ClaudeDiscoveryState{
				TranscriptPath: path,
			},
		}
		if payload.Source.ParentThreadID != "" {
			dr.IsForked = true
			dr.ForkParent = ProviderSessionID{
				Provider: ProviderCodex,
				ID:       payload.Source.ParentThreadID,
			}
		}
		return dr, true
	}
	return DiscoveryResult{}, false
}
