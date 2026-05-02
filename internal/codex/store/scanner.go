package codexstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DiscoveryScanner struct {
	Paths StorePaths
}

type DiscoveryResult struct {
	ThreadID      string
	ThreadName    string
	RolloutPath   string
	WorkspaceRoot string
	LatestWorkDir string
	Entrypoint    string
	ModelProvider string
	CreatedAt     time.Time
	UpdatedAt     time.Time
	ForkParentID  string
	IsSubagent    bool
	IsArchived    bool
}

func NewDiscoveryScanner(paths StorePaths) DiscoveryScanner {
	return DiscoveryScanner{Paths: paths}
}

func (s DiscoveryScanner) Scan() ([]DiscoveryResult, error) {
	idx, err := ReadSessionIndex(s.Paths.SessionIndexPath)
	if err != nil {
		return nil, err
	}
	var out []DiscoveryResult
	for _, root := range rolloutRoots(s.Paths) {
		scanned, err := scanRolloutRoot(root, idx)
		if err != nil {
			return nil, err
		}
		out = append(out, scanned...)
	}
	return out, nil
}

func scanRolloutRoot(root rolloutRoot, idx SessionIndex) ([]DiscoveryResult, error) {
	var out []DiscoveryResult
	err := filepath.WalkDir(root.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
				return nil
			}
			return err
		}
		if d.IsDir() || !isRolloutFilename(d.Name()) {
			return nil
		}
		thread, err := ReadThreadByRolloutPath(path, false, root.Archived)
		if err != nil {
			if id, createdAt, ok := rolloutIdentityFromFilename(d.Name()); ok {
				thread = ThreadSummary{
					ID:          id,
					RolloutPath: path,
					CreatedAt:   createdAt,
					IsArchived:  root.Archived,
				}
			} else {
				return nil
			}
		}
		name := idx.ThreadName(thread.ID)
		out = append(out, DiscoveryResult{
			ThreadID:      thread.ID,
			ThreadName:    name,
			RolloutPath:   path,
			WorkspaceRoot: thread.CWD,
			LatestWorkDir: firstNonEmpty(thread.LatestCWD, thread.CWD),
			Entrypoint:    thread.Originator,
			ModelProvider: thread.ModelProvider,
			CreatedAt:     thread.CreatedAt,
			UpdatedAt:     thread.UpdatedAt,
			ForkParentID:  thread.ForkedFromID,
			IsSubagent:    thread.IsSubagent,
			IsArchived:    thread.IsArchived,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func FindRolloutPathByThreadID(paths StorePaths, threadID string) (string, bool, error) {
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return "", false, nil
	}
	for _, root := range rolloutRoots(paths) {
		path, ok, err := findRolloutPathInRoot(root.Path, threadID)
		if err != nil {
			return "", false, err
		}
		if ok {
			return path, root.Archived, nil
		}
	}
	return "", false, nil
}

func findRolloutPathInRoot(root, threadID string) (string, bool, error) {
	var matchedPath string
	var matchedUpdated time.Time
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
				return nil
			}
			return err
		}
		if d.IsDir() || !isRolloutFilename(d.Name()) {
			return nil
		}
		if id, _, ok := rolloutIdentityFromFilename(d.Name()); ok && id != threadID {
			return nil
		}
		matches := false
		if id, _, ok := rolloutIdentityFromFilename(d.Name()); ok && id == threadID {
			matches = true
		} else if thread, err := ReadThreadByRolloutPath(path, false, false); err == nil && thread.ID == threadID {
			matches = true
		}
		if !matches {
			return nil
		}
		updated := time.Time{}
		if stat, err := os.Stat(path); err == nil {
			updated = stat.ModTime()
		}
		if matchedPath == "" || updated.After(matchedUpdated) {
			matchedPath = path
			matchedUpdated = updated
		}
		return nil
	})
	if err != nil {
		return "", false, err
	}
	return matchedPath, matchedPath != "", nil
}

func isRolloutFilename(name string) bool {
	return strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, ".jsonl")
}

func rolloutIdentityFromFilename(name string) (string, time.Time, bool) {
	if !isRolloutFilename(name) {
		return "", time.Time{}, false
	}
	base := strings.TrimSuffix(strings.TrimPrefix(name, "rollout-"), ".jsonl")
	if len(base) < len("2006-01-02T15-04-05-")+1 {
		return "", time.Time{}, false
	}
	timestampPart := base[:len("2006-01-02T15-04-05")]
	if len(base) <= len(timestampPart)+1 {
		return "", time.Time{}, false
	}
	threadID := base[len(timestampPart)+1:]
	createdAt, err := time.ParseInLocation("2006-01-02T15-04-05", timestampPart, time.UTC)
	if err != nil || strings.TrimSpace(threadID) == "" {
		return "", time.Time{}, false
	}
	return threadID, createdAt, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
