// Package artifacts dispatches session artifact cleanup to provider runtimes.
package artifacts

import (
	"context"
	"fmt"

	claudeartifacts "goodkind.io/clyde/internal/providers/claude/lifecycle/artifacts"
	codexlifecycle "goodkind.io/clyde/internal/providers/codex/lifecycle"
	"goodkind.io/clyde/internal/session"
)

// Delete removes provider-owned artifacts for a logical session row.
func Delete(ctx context.Context, req session.DeleteArtifactsRequest) (*session.DeletedArtifacts, error) {
	if req.Session == nil {
		return nil, fmt.Errorf("nil session")
	}
	switch req.Session.ProviderID() {
	case session.ProviderClaude:
		deleted, err := claudeartifacts.DeleteSessionArtifacts(req.ClydeRoot, req.Session)
		if err != nil {
			return nil, err
		}
		return &session.DeletedArtifacts{
			Transcripts: deleted.Transcript,
			AgentLogs:   deleted.AgentLogs,
		}, nil
	case session.ProviderCodex:
		return codexlifecycle.NewLifecycle().DeleteArtifacts(ctx, req)
	default:
		return nil, fmt.Errorf("unsupported session provider %q", req.Session.ProviderID())
	}
}
