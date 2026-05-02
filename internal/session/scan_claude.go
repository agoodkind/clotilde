package session

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type transcriptHeader struct {
	SessionID   string `json:"sessionId"`
	CWD         string `json:"cwd"`
	Entrypoint  string `json:"entrypoint"`
	Timestamp   string `json:"timestamp"`
	Type        string `json:"type"`
	Content     string `json:"content"`
	CustomTitle string `json:"customTitle"`
	ForkedFrom  struct {
		SessionID string `json:"sessionId"`
	} `json:"forkedFrom"`
}

type claudeDiscoveryScanner struct {
	projectsDir string
}

func newClaudeDiscoveryScanner(projectsDir string) DiscoveryScanner {
	return claudeDiscoveryScanner{projectsDir: projectsDir}
}

func (s claudeDiscoveryScanner) Provider() ProviderID {
	return ProviderClaude
}

func (s claudeDiscoveryScanner) Scan() ([]DiscoveryResult, error) {
	started := time.Now()
	var out []DiscoveryResult
	var withTitle int
	err := filepath.WalkDir(s.projectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsPermission(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		dr, ok := readClaudeTranscriptHeader(path)
		if !ok {
			return nil
		}
		if dr.CustomTitle != "" {
			withTitle++
		}
		out = append(out, dr)
		return nil
	})
	if err != nil {
		sessionScanLog.Logger().Warn("session.scan.walk_failed",
			"component", "session",
			"subcomponent", "scan",
			"provider", s.Provider(),
			"projects_dir", s.projectsDir,
			"err", err,
		)
		return nil, err
	}
	sessionScanLog.Logger().Debug("session.scan.completed",
		"component", "session",
		"subcomponent", "scan",
		"provider", s.Provider(),
		"projects_dir", s.projectsDir,
		"transcripts", len(out),
		"with_custom_title", withTitle,
		"duration_ms", time.Since(started).Milliseconds(),
	)
	return out, nil
}

func ScanProjects(claudeProjectsDir string) ([]DiscoveryResult, error) {
	return newClaudeDiscoveryScanner(claudeProjectsDir).Scan()
}

func ReadTranscriptHeader(path string) (DiscoveryResult, bool) {
	return readClaudeTranscriptHeader(path)
}

func readClaudeTranscriptHeader(path string) (DiscoveryResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveryResult{}, false
	}
	defer f.Close()

	dr := DiscoveryResult{
		Provider: ProviderClaude,
		Claude: ClaudeDiscoveryState{
			TranscriptPath: path,
		},
	}
	if strings.Contains(path, string(os.PathSeparator)+"subagents"+string(os.PathSeparator)) {
		dr.IsSubagent = true
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var h transcriptHeader
		if err := json.Unmarshal(line, &h); err != nil {
			continue
		}
		if h.Type == "queue-operation" {
			if !dr.IsAutoName && looksLikeAutoNamePrompt(h.Content) {
				dr.IsAutoName = true
			}
			continue
		}
		if h.Type == "custom-title" {
			if h.CustomTitle != "" {
				dr.CustomTitle = h.CustomTitle
			}
			if h.SessionID != "" && dr.ProviderIdentity().IsZero() {
				dr.Identity = ProviderSessionID{Provider: ProviderClaude, ID: h.SessionID}
			}
			if h.ForkedFrom.SessionID != "" && dr.ForkParent.IsZero() {
				dr.ForkParent = ProviderSessionID{Provider: ProviderClaude, ID: h.ForkedFrom.SessionID}
				dr.IsForked = true
			}
			continue
		}
		if h.SessionID != "" && dr.ProviderIdentity().IsZero() {
			dr.Identity = ProviderSessionID{Provider: ProviderClaude, ID: h.SessionID}
		}
		if h.ForkedFrom.SessionID != "" && dr.ForkParent.IsZero() {
			dr.ForkParent = ProviderSessionID{Provider: ProviderClaude, ID: h.ForkedFrom.SessionID}
			dr.IsForked = true
		}
		if h.CWD != "" && dr.WorkspaceRoot == "" {
			dr.WorkspaceRoot = h.CWD
		}
		if h.Entrypoint != "" && dr.Entrypoint == "" {
			dr.Entrypoint = h.Entrypoint
		}
		if h.Timestamp != "" && dr.FirstEntryTime.IsZero() {
			if t, err := time.Parse(time.RFC3339, h.Timestamp); err == nil {
				dr.FirstEntryTime = t
			}
		}
		if !dr.ProviderIdentity().IsZero() && dr.WorkspaceRoot != "" && dr.Entrypoint != "" && !dr.FirstEntryTime.IsZero() {
			break
		}
	}
	if dr.ProviderIdentity().IsZero() {
		return DiscoveryResult{}, false
	}
	if dr.Entrypoint == "sdk-cli" {
		dr.IsAutoName = true
	}
	return dr, true
}
