package transcript

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/fgrehm/clotilde/internal/config"
)

// BackupResult describes a transcript backup written before a compact op.
type BackupResult struct {
	Path  string // absolute path to the written backup file
	Bytes int64  // number of bytes copied
}

// MaxBackupsPerSession caps how many backups are retained per session.
// Older backups are pruned on each new backup. Set to 0 to disable pruning.
var MaxBackupsPerSession = 20

// BackupTranscript copies the transcript at srcPath into the backups
// directory, namespaced by session name. The destination is
//
//	$XDG_DATA_HOME/clotilde/backups/<sessionName>/<YYYYMMDD-HHMMSS>-<srcBase>
//
// If sessionName is empty, "unnamed" is used. After writing, the oldest
// backups for this session are pruned down to MaxBackupsPerSession.
func BackupTranscript(srcPath, sessionName string) (BackupResult, error) {
	if srcPath == "" {
		return BackupResult{}, fmt.Errorf("empty transcript path")
	}
	if _, err := os.Stat(srcPath); err != nil {
		return BackupResult{}, fmt.Errorf("stat transcript: %w", err)
	}

	if sessionName == "" {
		sessionName = "unnamed"
	}
	sessionName = sanitizeName(sessionName)

	dir := filepath.Join(config.GlobalBackupsDir(), sessionName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return BackupResult{}, fmt.Errorf("mkdir backup dir: %w", err)
	}

	base := filepath.Base(srcPath)
	ts := time.Now().UTC().Format("20060102-150405.000")
	dst := filepath.Join(dir, fmt.Sprintf("%s-%s", ts, base))

	n, err := copyFile(srcPath, dst)
	if err != nil {
		return BackupResult{}, err
	}

	if MaxBackupsPerSession > 0 {
		_ = pruneOldBackups(dir, MaxBackupsPerSession)
	}

	return BackupResult{Path: dst, Bytes: n}, nil
}

// copyFile copies src to dst atomically via a temp file + rename.
func copyFile(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, fmt.Errorf("open source: %w", err)
	}
	defer in.Close()

	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, fmt.Errorf("create temp: %w", err)
	}
	n, copyErr := io.Copy(out, in)
	if copyErr != nil {
		out.Close()
		os.Remove(tmp)
		return n, fmt.Errorf("copy: %w", copyErr)
	}
	if syncErr := out.Sync(); syncErr != nil {
		out.Close()
		os.Remove(tmp)
		return n, fmt.Errorf("sync: %w", syncErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		os.Remove(tmp)
		return n, fmt.Errorf("close: %w", closeErr)
	}
	if renameErr := os.Rename(tmp, dst); renameErr != nil {
		os.Remove(tmp)
		return n, fmt.Errorf("rename: %w", renameErr)
	}
	return n, nil
}

// pruneOldBackups keeps at most keep most-recent files in dir, deleting the rest.
// It only considers regular files whose names start with an 8-digit date prefix
// (YYYYMMDD), so unrelated files are never removed.
func pruneOldBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type fileEntry struct {
		name string
		mod  time.Time
	}
	files := make([]fileEntry, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if len(name) < 8 || !isDigits(name[:8]) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, fileEntry{name: name, mod: info.ModTime()})
	}
	if len(files) <= keep {
		return nil
	}
	// Sort newest first
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for _, f := range files[keep:] {
		_ = os.Remove(filepath.Join(dir, f.name))
	}
	return nil
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeName replaces filesystem-unfriendly characters in a session name.
func sanitizeName(s string) string {
	replacer := strings.NewReplacer(
		"/", "_",
		`\`, "_",
		":", "_",
		" ", "_",
	)
	out := replacer.Replace(s)
	if out == "" || out == "." || out == ".." {
		return "unnamed"
	}
	return out
}
