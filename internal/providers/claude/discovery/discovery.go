// Package discovery scans Claude transcript artifacts for adoptable sessions.
package discovery

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goodkind.io/clyde/internal/session"
)

const primaryArtifactKindTranscript = "transcript"

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

type Scanner struct {
	projectsDir string
}

func NewScanner(projectsDir string) Scanner {
	return Scanner{projectsDir: projectsDir}
}

func (s Scanner) Provider() session.ProviderID {
	return session.ProviderClaude
}

func (s Scanner) Scan() ([]session.DiscoveryResult, error) {
	var out []session.DiscoveryResult
	err := filepath.WalkDir(s.projectsDir, func(path string, directoryEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsPermission(walkErr) {
				return nil
			}
			return walkErr
		}
		if directoryEntry.IsDir() {
			return nil
		}
		if !strings.HasSuffix(directoryEntry.Name(), ".jsonl") {
			return nil
		}
		discoveryResult, ok := ReadTranscriptHeader(path)
		if !ok {
			return nil
		}
		out = append(out, discoveryResult)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s Scanner) DiscoveryScannerForHome(homeDir string) session.DiscoveryScanner {
	return NewScanner(ProjectsRoot(homeDir))
}

func (s Scanner) DiscoveryScannerForRoot(root string) session.DiscoveryScanner {
	return NewScanner(root)
}

func ProjectsRoot(homeDir string) string {
	return filepath.Join(homeDir, ".claude", "projects")
}

func ReadTranscriptHeader(path string) (session.DiscoveryResult, bool) {
	file, err := os.Open(path)
	if err != nil {
		return session.DiscoveryResult{}, false
	}
	defer func() { _ = file.Close() }()

	discoveryResult := session.DiscoveryResult{
		Provider:            session.ProviderClaude,
		PrimaryArtifact:     path,
		PrimaryArtifactKind: primaryArtifactKindTranscript,
	}
	if strings.Contains(path, string(os.PathSeparator)+"subagents"+string(os.PathSeparator)) {
		discoveryResult.IsSubagent = true
	}

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var header transcriptHeader
		if err := json.Unmarshal(line, &header); err != nil {
			continue
		}
		if header.Type == "queue-operation" {
			if !discoveryResult.IsAutoName && looksLikeAutoNamePrompt(header.Content) {
				discoveryResult.IsAutoName = true
			}
			continue
		}
		if header.Type == "custom-title" {
			if header.CustomTitle != "" {
				discoveryResult.CustomTitle = header.CustomTitle
			}
			if header.SessionID != "" && discoveryResult.ProviderIdentity().IsZero() {
				discoveryResult.Identity = session.ProviderSessionID{Provider: session.ProviderClaude, ID: header.SessionID}
			}
			if header.ForkedFrom.SessionID != "" && discoveryResult.ForkParent.IsZero() {
				discoveryResult.ForkParent = session.ProviderSessionID{Provider: session.ProviderClaude, ID: header.ForkedFrom.SessionID}
				discoveryResult.IsForked = true
			}
			continue
		}
		if header.SessionID != "" && discoveryResult.ProviderIdentity().IsZero() {
			discoveryResult.Identity = session.ProviderSessionID{Provider: session.ProviderClaude, ID: header.SessionID}
		}
		if header.ForkedFrom.SessionID != "" && discoveryResult.ForkParent.IsZero() {
			discoveryResult.ForkParent = session.ProviderSessionID{Provider: session.ProviderClaude, ID: header.ForkedFrom.SessionID}
			discoveryResult.IsForked = true
		}
		if header.CWD != "" && discoveryResult.WorkspaceRoot == "" {
			discoveryResult.WorkspaceRoot = header.CWD
		}
		if header.Entrypoint != "" && discoveryResult.Entrypoint == "" {
			discoveryResult.Entrypoint = header.Entrypoint
		}
		if header.Timestamp != "" && discoveryResult.FirstEntryTime.IsZero() {
			if parsedTime, parseErr := time.Parse(time.RFC3339, header.Timestamp); parseErr == nil {
				discoveryResult.FirstEntryTime = parsedTime
			}
		}
		if !discoveryResult.ProviderIdentity().IsZero() && discoveryResult.WorkspaceRoot != "" && discoveryResult.Entrypoint != "" && !discoveryResult.FirstEntryTime.IsZero() {
			break
		}
	}
	if discoveryResult.ProviderIdentity().IsZero() {
		return session.DiscoveryResult{}, false
	}
	if discoveryResult.Entrypoint == "sdk-cli" {
		discoveryResult.IsAutoName = true
	}
	return discoveryResult, true
}

func looksLikeAutoNamePrompt(content string) bool {
	if content == "" {
		return false
	}
	lowercaseContent := strings.ToLower(content)
	return strings.Contains(lowercaseContent, "kebab-case") && strings.Contains(lowercaseContent, "output only")
}
