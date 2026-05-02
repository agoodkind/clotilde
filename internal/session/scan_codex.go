package session

import (
	"path/filepath"

	codexstore "goodkind.io/clyde/internal/codex/store"
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
	paths, err := codexstore.ResolveStorePaths(s.codexHome, "")
	if err != nil {
		return nil, err
	}
	results, err := codexstore.NewDiscoveryScanner(paths).Scan()
	if err != nil {
		return nil, err
	}
	out := make([]DiscoveryResult, 0, len(results))
	for _, result := range results {
		out = append(out, codexDiscoveryResult(result))
	}
	return out, nil
}

func codexDiscoveryResult(result codexstore.DiscoveryResult) DiscoveryResult {
	workspace := result.LatestWorkDir
	if workspace == "" {
		workspace = result.WorkspaceRoot
	}
	return DiscoveryResult{
		Provider: ProviderCodex,
		Identity: ProviderSessionID{
			Provider: ProviderCodex,
			ID:       result.ThreadID,
		},
		WorkspaceRoot:  workspace,
		Entrypoint:     result.Entrypoint,
		FirstEntryTime: result.CreatedAt,
		CustomTitle:    result.ThreadName,
		ForkParent: ProviderSessionID{
			Provider: ProviderCodex,
			ID:       result.ForkParentID,
		},
		IsForked:   result.ForkParentID != "",
		IsSubagent: result.IsSubagent,
		Claude: ClaudeDiscoveryState{
			TranscriptPath: result.RolloutPath,
		},
	}
}

func defaultCodexHome(homeDir string) string {
	return filepath.Join(homeDir, ".codex")
}
