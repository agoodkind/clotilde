package codexstore

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// SessionIndexEntry is the typed append-only row Codex writes to
// CODEX_HOME/session_index.jsonl. The latest row wins for name/id lookups.
type SessionIndexEntry struct {
	ID         string `json:"id"`
	ThreadName string `json:"thread_name"`
	UpdatedAt  string `json:"updated_at"`
}

// SessionIndex holds the latest known thread names from Codex's append-only
// session index.
type SessionIndex struct {
	entriesByID []SessionIndexEntry
}

func ReadSessionIndex(path string) (SessionIndex, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return SessionIndex{}, nil
		}
		return SessionIndex{}, err
	}
	defer func() { _ = f.Close() }()

	latest := make(map[string]SessionIndexEntry)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var entry SessionIndexEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		entry.ID = strings.TrimSpace(entry.ID)
		entry.ThreadName = strings.TrimSpace(entry.ThreadName)
		if entry.ID == "" || entry.ThreadName == "" {
			continue
		}
		latest[entry.ID] = entry
	}
	if err := scanner.Err(); err != nil {
		return SessionIndex{}, err
	}
	out := make([]SessionIndexEntry, 0, len(latest))
	for _, entry := range latest {
		out = append(out, entry)
	}
	return SessionIndex{entriesByID: out}, nil
}

func (idx SessionIndex) ThreadName(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	for _, entry := range idx.entriesByID {
		if entry.ID == id {
			return entry.ThreadName
		}
	}
	return ""
}
