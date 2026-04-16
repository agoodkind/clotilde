package transcript

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fgrehm/clotilde/internal/config"
)

// CachedStats holds a persisted QuickStats result for a transcript.
type CachedStats struct {
	Stats   CompactQuickStats `json:"stats"`
	ModTime time.Time         `json:"mod_time"`
	Path    string            `json:"path"`
}

// StatsCacheDir returns the directory where per-transcript stats caches are stored.
func StatsCacheDir() string {
	return filepath.Join(config.GlobalCacheDir(), "stats")
}

// cacheKeyPath returns the path of the JSON cache file for the given transcript path.
// Uses SHA256 of the transcript path so the filename is filesystem-safe.
func cacheKeyPath(transcriptPath string) string {
	sum := sha256.Sum256([]byte(transcriptPath))
	return filepath.Join(StatsCacheDir(), fmt.Sprintf("%x.json", sum))
}

// LoadCachedStats returns the cached CompactQuickStats for the given transcript
// if the cache exists and the transcript file mtime matches. Returns nil when
// the cache is absent or stale.
func LoadCachedStats(transcriptPath string) *CachedStats {
	info, err := os.Stat(transcriptPath)
	if err != nil {
		return nil
	}
	currentMtime := info.ModTime().UTC().Truncate(time.Second)

	cachePath := cacheKeyPath(transcriptPath)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil
	}

	var cached CachedStats
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil
	}

	// Stale if the transcript has been modified since we cached it.
	if !cached.ModTime.UTC().Truncate(time.Second).Equal(currentMtime) {
		return nil
	}

	return &cached
}

// SaveCachedStats writes the stats to disk atomically (write tmp + rename).
// Errors are silently ignored so cache failures never affect the caller.
func SaveCachedStats(transcriptPath string, stats CompactQuickStats, modTime time.Time) {
	if err := os.MkdirAll(StatsCacheDir(), 0o755); err != nil {
		return
	}

	cached := CachedStats{
		Stats:   stats,
		ModTime: modTime.UTC().Truncate(time.Second),
		Path:    transcriptPath,
	}

	data, err := json.Marshal(cached)
	if err != nil {
		return
	}

	cachePath := cacheKeyPath(transcriptPath)
	tmpPath := cachePath + ".tmp"

	if err := os.WriteFile(tmpPath, data, 0o644); err != nil {
		return
	}

	// Atomic rename: readers either see the old or new file, never a partial write.
	_ = os.Rename(tmpPath, cachePath)
}
