// Package artifacts dispatches session artifact cleanup to provider runtimes.
package artifacts

import (
	"context"
	"fmt"

	claudeartifacts "goodkind.io/clyde/internal/claude/artifacts"
	"goodkind.io/clyde/internal/session"
)

// Delete removes provider-owned artifacts for a logical session row.
func Delete(_ context.Context, req session.DeleteArtifactsRequest) error {
	if req.Session == nil {
		return fmt.Errorf("nil session")
	}
	switch req.Session.ProviderID() {
	case session.ProviderClaude:
		_, err := claudeartifacts.DeleteSessionArtifacts(req.ClydeRoot, req.Session)
		return err
	default:
		return fmt.Errorf("unsupported session provider %q", req.Session.ProviderID())
	}
}
