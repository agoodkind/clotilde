package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DiscoveryResult captures the outcome of a single transcript discovery.
// The TranscriptPath is always populated; the rest depend on whether the
// first entry could be parsed.
type DiscoveryResult struct {
	TranscriptPath string
	SessionID      string
	WorkspaceRoot  string
	Entrypoint     string
	FirstEntryTime time.Time
	IsAutoName     bool // SDK-CLI invocation that looks like a clyde auto-name call
	IsSubagent     bool // file lives in a subagents/ directory
}

// AdoptedSession is the registry entry created for a previously-unknown
// transcript. It includes the auto-generated name so callers can report.
type AdoptedSession struct {
	Name     string
	Metadata Metadata
}

// scratchDirSuffixes lists workspace-root path fragments produced by
// clyde-internal subprocess invocations. Discovery skips any
// transcript whose cwd matches one of these so the user's session
// list never fills with adapter or context-summary noise.
var scratchDirSuffixes = []string{
	"/Library/Caches/clotilde/context-scratch",
	"/.cache/clotilde/context-scratch",
	"/Library/Caches/clotilde/adapter-scratch",
	"/.cache/clotilde/adapter-scratch",
	"/Library/Caches/clyde/context-scratch",
	"/.cache/clyde/context-scratch",
	"/Library/Caches/clyde/adapter-scratch",
	"/.cache/clyde/adapter-scratch",
}

// isClydeScratch reports whether path looks like a clyde owned
// scratch directory used to anchor internal claude -p calls. The
// match is suffix based so it works whether the user's home is at
// /Users/foo or /home/foo or anywhere else.
func isClydeScratch(path string) bool {
	if path == "" {
		return false
	}
	for _, s := range scratchDirSuffixes {
		if strings.HasSuffix(path, s) {
			return true
		}
	}
	return false
}

// transcriptHeader is the minimum subset we need from the first entry of a
// jsonl transcript to map it into the registry.
type transcriptHeader struct {
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	Entrypoint string `json:"entrypoint"`
	Timestamp  string `json:"timestamp"`
	Type       string `json:"type"`
	Content    string `json:"content"` // present on queue-operation entries
}

// ScanProjects walks ~/.claude/projects/<encoded-cwd>/*.jsonl and returns
// one DiscoveryResult per transcript. Subagent transcripts (anywhere under
// a subagents/ directory) are flagged but still returned so callers can
// decide whether to surface them. The walk is best-effort: unreadable
// files are skipped silently.
func ScanProjects(claudeProjectsDir string) ([]DiscoveryResult, error) {
	var out []DiscoveryResult
	err := filepath.WalkDir(claudeProjectsDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// Skip permission errors but keep walking other branches.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		dr, ok := readTranscriptHeader(path)
		if !ok {
			return nil
		}
		out = append(out, dr)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// readTranscriptHeader reads enough of a jsonl transcript to identify the
// session it belongs to. The function returns ok=false when the file is
// unreadable or contains no recognizable entries.
func readTranscriptHeader(path string) (DiscoveryResult, bool) {
	f, err := os.Open(path)
	if err != nil {
		return DiscoveryResult{}, false
	}
	defer f.Close()

	dr := DiscoveryResult{TranscriptPath: path}
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
		// queue-operation entries come first in clyde-wrapped sessions
		// and carry the auto-name prompt as their content. They never
		// have a cwd or entrypoint so they cannot stand alone.
		if h.Type == "queue-operation" {
			if dr.IsAutoName == false && looksLikeAutoNamePrompt(h.Content) {
				dr.IsAutoName = true
			}
			continue
		}
		if h.SessionID != "" && dr.SessionID == "" {
			dr.SessionID = h.SessionID
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
		if dr.SessionID != "" && dr.WorkspaceRoot != "" && dr.Entrypoint != "" && !dr.FirstEntryTime.IsZero() {
			break
		}
	}
	if dr.SessionID == "" {
		return DiscoveryResult{}, false
	}
	if dr.Entrypoint == "sdk-cli" {
		dr.IsAutoName = true
	}
	return dr, true
}

// looksLikeAutoNamePrompt heuristically detects the prompts clyde
// dispatches to haiku for session naming. The prompt always asks for a
// kebab-case label and includes the words "kebab-case" and "Output ONLY".
func looksLikeAutoNamePrompt(content string) bool {
	if content == "" {
		return false
	}
	c := strings.ToLower(content)
	return strings.Contains(c, "kebab-case") && strings.Contains(c, "output only")
}

// AdoptUnknown creates registry stubs for transcripts that no existing
// session knows about. Sessions that are tagged as auto-name or subagent
// are skipped so the dashboard does not fill with noise. The function
// returns the list of adopted sessions.
func AdoptUnknown(store *FileStore, results []DiscoveryResult) ([]AdoptedSession, error) {
	known, err := buildKnownUUIDSet(store)
	if err != nil {
		return nil, err
	}
	existingNames, err := buildExistingNameSet(store)
	if err != nil {
		return nil, err
	}

	var adopted []AdoptedSession
	for _, r := range results {
		if r.IsAutoName || r.IsSubagent {
			continue
		}
		if isClydeScratch(r.WorkspaceRoot) {
			continue
		}
		if r.SessionID == "" {
			continue
		}
		if known[r.SessionID] {
			continue
		}
		name := uniqueAdoptedName(r, existingNames)
		existingNames[name] = true

		md := Metadata{
			Name:           name,
			SessionID:      r.SessionID,
			TranscriptPath: r.TranscriptPath,
			WorkspaceRoot:  r.WorkspaceRoot,
			WorkDir:        r.WorkspaceRoot,
		}
		fi, err := os.Stat(r.TranscriptPath)
		if err == nil {
			md.LastAccessed = fi.ModTime()
		}
		if !r.FirstEntryTime.IsZero() {
			md.Created = r.FirstEntryTime
		} else if !md.LastAccessed.IsZero() {
			md.Created = md.LastAccessed
		} else {
			md.Created = time.Now()
		}
		if md.LastAccessed.IsZero() {
			md.LastAccessed = md.Created
		}

		sess := &Session{Name: name, Metadata: md}
		if err := store.Create(sess); err != nil {
			continue
		}
		adopted = append(adopted, AdoptedSession{Name: name, Metadata: md})
		known[r.SessionID] = true
	}
	return adopted, nil
}

// buildKnownUUIDSet returns the set of UUIDs the store already manages.
// Both current and previous IDs are included so a session that has gone
// through /clear cycles is not double-adopted.
func buildKnownUUIDSet(store *FileStore) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all)*2)
	for _, s := range all {
		if s.Metadata.SessionID != "" {
			out[s.Metadata.SessionID] = true
		}
		for _, id := range s.Metadata.PreviousSessionIDs {
			out[id] = true
		}
	}
	return out, nil
}

func buildExistingNameSet(store *FileStore) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, s := range all {
		out[s.Name] = true
	}
	return out, nil
}

// uniqueAdoptedName generates a registry-safe name for an adopted
// transcript. The base is a sanitized basename of the workspace root
// joined with the first eight characters of the session UUID. Collisions
// are resolved by appending a counter.
func uniqueAdoptedName(r DiscoveryResult, taken map[string]bool) string {
	base := workspaceBaseName(r.WorkspaceRoot)
	short := safeShortUUID(r.SessionID)
	candidate := fmt.Sprintf("%s-%s", base, short)
	if !taken[candidate] {
		return candidate
	}
	for i := 2; i < 1000; i++ {
		c := fmt.Sprintf("%s-%s-%d", base, short, i)
		if !taken[c] {
			return c
		}
	}
	return candidate
}

func workspaceBaseName(root string) string {
	if root == "" {
		return "adopted"
	}
	base := filepath.Base(root)
	base = strings.ToLower(base)
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	if b.Len() == 0 {
		return "adopted"
	}
	return b.String()
}

func safeShortUUID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}
