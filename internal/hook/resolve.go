package hook

import (
	"fmt"
	"os"
	"slices"

	"goodkind.io/clyde/internal/session"
)

func resolveSessionName(hookData SessionStartInput, store session.Store, fullFallback bool) (string, error) {
	if name := os.Getenv("CLYDE_SESSION_NAME"); name != "" {
		return name, nil
	}

	if !fullFallback {
		return "", nil
	}

	if name := readLastEnvFileValue("CLYDE_SESSION"); name != "" {
		return name, nil
	}

	return findSessionByUUID(store, hookData.SessionID)
}

func findSessionByUUID(store session.Store, uuid string) (string, error) {
	sessions, err := store.List()
	if err != nil {
		hookLog.Warn("hook.resolve_session.list_failed",
			"component", "hook",
			"subcomponent", "resolve",
			"err", err,
		)
		return "", fmt.Errorf("failed to list sessions: %w", err)
	}

	for _, sess := range sessions {
		if sess.Metadata.ProviderSessionID() == uuid {
			return sess.Name, nil
		}
	}

	for _, sess := range sessions {
		if slices.Contains(sess.Metadata.PreviousProviderSessionIDStrings(), uuid) {
			return sess.Name, nil
		}
	}

	hookLog.Warn("hook.resolve_session.uuid_not_found",
		"component", "hook",
		"subcomponent", "resolve",
		"session_id", uuid,
	)
	return "", fmt.Errorf("no session found with UUID %s", uuid)
}
