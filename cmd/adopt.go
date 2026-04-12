package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/util"
)

// adoptTranscriptInfo holds information extracted from a Claude Code transcript.
type adoptTranscriptInfo struct {
	SessionID      string
	CWD            string
	TranscriptPath string
	ExtractedName  string
	Created        time.Time
	LastAccessed   time.Time
	ProjectDir     string // basename of ~/.claude/projects/<dir>
}

// adoptLine is a partial parse of a single JSONL line in a transcript.
type adoptLine struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

var adoptSessionNameRe = regexp.MustCompile(`Session name: ([^\n]+)`)

func newAdoptCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "adopt",
		Short: "Import untracked Claude Code sessions into clotilde",
		Long: `Scan ~/.claude/projects/ for Claude Code sessions that are not yet tracked
by clotilde, and register them.

Each transcript is read to extract the working directory and session name,
then the metadata is written under .claude/clotilde/ at the appropriate project root.`,
		RunE: runAdopt,
	}
	cmd.Flags().Bool("dry-run", false, "Show what would be adopted without making changes")
	return cmd
}

func runAdopt(cmd *cobra.Command, _ []string) error {
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	home, err := util.HomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	if _, statErr := os.Stat(projectsDir); os.IsNotExist(statErr) {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No Claude Code projects directory found.")
		return nil
	}

	transcripts, knownUUIDs, err := adoptScanAndIndex(projectsDir)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	// Collect unknown sessions, deduplicating by UUID.
	var toAdopt []*adoptTranscriptInfo
	seen := make(map[string]bool)
	for _, t := range transcripts {
		if t.SessionID == "" || knownUUIDs[t.SessionID] || seen[t.SessionID] {
			continue
		}
		seen[t.SessionID] = true
		toAdopt = append(toAdopt, t)
	}

	if len(toAdopt) == 0 {
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "All sessions are already tracked.")
		return nil
	}

	if dryRun {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Would adopt %d session(s):\n", len(toAdopt))
		for _, t := range toAdopt {
			name := adoptChooseName(t, nil)
			id := t.SessionID
			if len(id) > 8 {
				id = id[:8]
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  %-10s  %-30s  %s\n", id+"...", name, t.CWD)
		}
		return nil
	}

	adopted := 0
	for _, t := range toAdopt {
		name, root, adoptErr := adoptOne(t)
		if adoptErr != nil {
			id := t.SessionID
			if len(id) > 8 {
				id = id[:8]
			}
			_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "  skip %s...: %v\n", id, adoptErr)
			continue
		}
		adopted++
		knownUUIDs[t.SessionID] = true
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "  adopted '%s'  (%s)\n", name, root)
	}

	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "\nAdopted %d of %d session(s).\n", adopted, len(toAdopt))
	return nil
}

// adoptScanAndIndex scans projectsDir for all transcripts and builds the set of
// UUIDs already tracked by reachable clotilde stores.
func adoptScanAndIndex(projectsDir string) ([]*adoptTranscriptInfo, map[string]bool, error) {
	knownUUIDs := make(map[string]bool)
	loadedRoots := make(map[string]bool)
	var transcripts []*adoptTranscriptInfo

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		files, readErr := os.ReadDir(projDir)
		if readErr != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fname := f.Name()
			// Skip agent logs and non-JSONL files.
			if strings.HasPrefix(fname, "agent-") || !strings.HasSuffix(fname, ".jsonl") {
				continue
			}

			tPath := filepath.Join(projDir, fname)
			info, extractErr := adoptExtractInfo(tPath, entry.Name())
			if extractErr != nil || info.SessionID == "" {
				continue
			}
			transcripts = append(transcripts, info)

			// Load known UUIDs from the clotilde root for this transcript's CWD.
			if info.CWD != "" {
				root, rootErr := config.ClotildeRootFromPath(info.CWD)
				if rootErr == nil && !loadedRoots[root] {
					loadedRoots[root] = true
					store := session.NewFileStore(root)
					sessions, listErr := store.List()
					if listErr == nil {
						for _, s := range sessions {
							knownUUIDs[s.Metadata.SessionID] = true
							for _, prev := range s.Metadata.PreviousSessionIDs {
								if prev != "" {
									knownUUIDs[prev] = true
								}
							}
						}
					}
				}
			}
		}
	}

	return transcripts, knownUUIDs, nil
}

// adoptExtractInfo reads the first part of a JSONL transcript to pull out session
// metadata: sessionId, cwd, creation timestamp, and optionally a session name if
// the clotilde SessionStart hook previously wrote one.
func adoptExtractInfo(path, projectDirName string) (*adoptTranscriptInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, statErr := f.Stat()
	if statErr != nil {
		return nil, statErr
	}

	info := &adoptTranscriptInfo{
		TranscriptPath: path,
		ProjectDir:     projectDirName,
		LastAccessed:   fi.ModTime(),
	}

	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 2*1024*1024)

	lineNum := 0
	for scanner.Scan() && lineNum < 200 {
		lineNum++
		rawLine := scanner.Text()
		if rawLine == "" {
			continue
		}

		var parsed adoptLine
		if jsonErr := json.Unmarshal([]byte(rawLine), &parsed); jsonErr != nil {
			continue
		}

		if info.SessionID == "" && parsed.SessionID != "" {
			info.SessionID = parsed.SessionID
		}
		if info.CWD == "" && parsed.CWD != "" {
			info.CWD = parsed.CWD
		}
		if info.Created.IsZero() && parsed.Timestamp != "" {
			if t, tErr := time.Parse(time.RFC3339Nano, parsed.Timestamp); tErr == nil {
				info.Created = t
			} else if t, tErr := time.Parse(time.RFC3339, parsed.Timestamp); tErr == nil {
				info.Created = t
			}
		}
		if info.ExtractedName == "" && parsed.Content != "" {
			if m := adoptSessionNameRe.FindStringSubmatch(parsed.Content); m != nil {
				info.ExtractedName = strings.TrimSpace(m[1])
			}
		}

		// Stop early once we have all we need.
		if info.SessionID != "" && info.CWD != "" && info.ExtractedName != "" {
			break
		}
	}

	return info, nil
}

// adoptOne registers a single untracked transcript as a clotilde session.
// Returns the session name and clotilde root it was registered under.
func adoptOne(t *adoptTranscriptInfo) (string, string, error) {
	clotildeRoot, err := adoptResolveRoot(t)
	if err != nil {
		return "", "", err
	}

	store := session.NewFileStore(clotildeRoot)
	name := adoptChooseName(t, store)

	created := t.LastAccessed
	if !t.Created.IsZero() {
		created = t.Created
	}

	sess := &session.Session{
		Name: name,
		Metadata: session.Metadata{
			Name:           name,
			SessionID:      t.SessionID,
			TranscriptPath: t.TranscriptPath,
			WorkDir:        t.CWD,
			Created:        created,
			LastAccessed:   t.LastAccessed,
		},
	}

	if err := store.Create(sess); err != nil {
		return "", "", fmt.Errorf("creating session: %w", err)
	}

	return name, clotildeRoot, nil
}

// adoptResolveRoot determines the clotilde root for a transcript.
// Walks up from the transcript CWD looking for an existing .claude/clotilde.
// If none found, creates one at the CWD.
func adoptResolveRoot(t *adoptTranscriptInfo) (string, error) {
	cwd := t.CWD
	if cwd == "" {
		home, err := util.HomeDir()
		if err != nil {
			return "", fmt.Errorf("no cwd and cannot determine home: %w", err)
		}
		cwd = home
	}

	if root, err := config.ClotildeRootFromPath(cwd); err == nil {
		return root, nil
	}

	// No existing root — create one at the cwd.
	if err := config.EnsureSessionsDir(cwd); err != nil {
		return "", fmt.Errorf("cannot create .claude/clotilde at %s: %w", cwd, err)
	}
	return filepath.Join(cwd, config.ClotildeDir), nil
}

// adoptChooseName returns a unique, valid session name for the transcript.
// store may be nil (dry-run / no conflict checking needed).
func adoptChooseName(t *adoptTranscriptInfo, store session.Store) string {
	var candidates []string

	// Priority 1: name the clotilde hook previously injected into the session.
	if t.ExtractedName != "" {
		if sanitized := adoptSanitizeName(t.ExtractedName); sanitized != "" {
			candidates = append(candidates, sanitized)
		}
	}

	// Priority 2: last path component of the project dir.
	if t.ProjectDir != "" {
		if derived := adoptDeriveFromProjectDir(t.ProjectDir); derived != "" {
			candidates = append(candidates, derived)
		}
	}

	// Priority 3: UUID short prefix.
	if len(t.SessionID) >= 8 {
		candidates = append(candidates, "session-"+t.SessionID[:8])
	}

	for _, c := range candidates {
		if session.ValidateName(c) != nil {
			continue
		}
		if store == nil || !store.Exists(c) {
			return c
		}
		// Conflict — append UUID suffix.
		if len(t.SessionID) >= 8 {
			suffixed := c + "-" + t.SessionID[:8]
			if session.ValidateName(suffixed) == nil && (store == nil || !store.Exists(suffixed)) {
				return suffixed
			}
		}
	}

	return "adopted-" + t.SessionID[:8]
}

// adoptSanitizeName converts an arbitrary string to a valid session name, or
// returns "" if it cannot be made valid.
func adoptSanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevHyphen := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen && b.Len() > 0 {
			b.WriteRune('-')
			prevHyphen = true
		}
	}
	result := strings.TrimRight(b.String(), "-")
	if len(result) < session.MinNameLength || len(result) > session.MaxNameLength {
		return ""
	}
	if session.ValidateName(result) != nil {
		return ""
	}
	return result
}

// adoptDeriveFromProjectDir converts a Claude Code project directory name (e.g.
// "-Users-alex-Sites-myproject") to a short human-friendly session name.
func adoptDeriveFromProjectDir(dirName string) string {
	// Strip leading dashes, split on dash, return last meaningful segment.
	trimmed := strings.TrimLeft(dirName, "-")
	parts := strings.Split(trimmed, "-")
	for i := len(parts) - 1; i >= 0; i-- {
		p := strings.ToLower(parts[i])
		if p == "" {
			continue
		}
		if session.ValidateName(p) == nil {
			return p
		}
	}
	return ""
}

// tryAdoptByUUID scans all transcripts for one whose sessionId matches targetUUID,
// adopts it, and returns the session name and clotilde root it was registered under.
func tryAdoptByUUID(targetUUID string) (string, string, error) {
	home, err := util.HomeDir()
	if err != nil {
		return "", "", fmt.Errorf("cannot determine home directory: %w", err)
	}

	projectsDir := filepath.Join(home, ".claude", "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", "", fmt.Errorf("reading projects dir: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		projDir := filepath.Join(projectsDir, entry.Name())
		files, readErr := os.ReadDir(projDir)
		if readErr != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() {
				continue
			}
			fname := f.Name()
			if strings.HasPrefix(fname, "agent-") || !strings.HasSuffix(fname, ".jsonl") {
				continue
			}

			tPath := filepath.Join(projDir, fname)
			info, extractErr := adoptExtractInfo(tPath, entry.Name())
			if extractErr != nil || info.SessionID != targetUUID {
				continue
			}

			// Found the matching transcript; adopt it.
			name, root, adoptErr := adoptOne(info)
			if adoptErr != nil {
				return "", "", fmt.Errorf("auto-adopt: %w", adoptErr)
			}
			return name, root, nil
		}
	}

	return "", "", fmt.Errorf("no transcript found for UUID %s", targetUUID)
}
