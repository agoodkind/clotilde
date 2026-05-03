package codex

import (
	"context"
	"errors"
	"os"
	"strings"

	codexstore "goodkind.io/clyde/internal/providers/codex/store"
	"goodkind.io/clyde/internal/session"
)

func (l *Lifecycle) DeleteArtifacts(_ context.Context, req session.DeleteArtifactsRequest) (*session.DeletedArtifacts, error) {
	if req.Session == nil {
		return nil, errors.New("nil session")
	}
	paths, err := codexstore.ResolveStorePathsFromEnv()
	if err != nil {
		return nil, err
	}
	deleted := &session.DeletedArtifacts{}
	seen := make(map[string]bool)
	for _, path := range knownArtifactPaths(req.Session, paths) {
		if deletePathOnce(path, seen) {
			deleted.Transcripts = append(deleted.Transcripts, path)
		}
	}
	for _, threadID := range knownThreadIDs(req.Session) {
		path, _, err := codexstore.FindRolloutPathByThreadID(paths, threadID)
		if err != nil {
			return deleted, err
		}
		if deletePathOnce(path, seen) {
			deleted.Transcripts = append(deleted.Transcripts, path)
		}
	}
	return deleted, nil
}

func knownArtifactPaths(sess *session.Session, paths codexstore.StorePaths) []string {
	if sess == nil {
		return nil
	}
	var out []string
	if path := strings.TrimSpace(sess.Metadata.ProviderTranscriptPath()); path != "" {
		out = append(out, path)
	}
	for _, threadID := range knownThreadIDs(sess) {
		if path, _, err := codexstore.FindRolloutPathByThreadID(paths, threadID); err == nil && path != "" {
			out = append(out, path)
		}
	}
	return out
}

func knownThreadIDs(sess *session.Session) []string {
	if sess == nil {
		return nil
	}
	ids := []string{sess.Metadata.ProviderSessionID()}
	ids = append(ids, sess.Metadata.PreviousProviderSessionIDStrings()...)
	out := make([]string, 0, len(ids))
	seen := make(map[string]bool)
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

func deletePathOnce(path string, seen map[string]bool) bool {
	path = strings.TrimSpace(path)
	if path == "" || seen[path] {
		return false
	}
	seen[path] = true
	if err := os.Remove(path); err != nil {
		return false
	}
	return true
}
