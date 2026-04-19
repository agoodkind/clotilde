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
		return "", fmt.Errorf("failed to list sessions: %w", err)
	}

	for _, sess := range sessions {
		if sess.Metadata.SessionID == uuid {
			return sess.Name, nil
		}
	}

	for _, sess := range sessions {
		if slices.Contains(sess.Metadata.PreviousSessionIDs, uuid) {
			return sess.Name, nil
		}
	}

	return "", fmt.Errorf("no session found with UUID %s", uuid)
}
