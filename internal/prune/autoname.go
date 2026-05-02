package prune

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/session"
)

const defaultAutonameMinAge = time.Hour

// buildKnownTranscriptPaths returns transcript paths referenced by any tracked session.
func buildKnownTranscriptPaths(store session.Store) (map[string]bool, error) {
	all, err := store.List()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(all))
	for _, sess := range all {
		if sess.Metadata.ProviderTranscriptPath() != "" {
			out[sess.Metadata.ProviderTranscriptPath()] = true
		}
	}
	for path := range out {
		base := filepath.Base(path)
		dir := filepath.Dir(path)
		alt := filepath.Join(dir, strings.TrimSpace(base))
		out[alt] = true
	}
	return out, nil
}

// PruneAutoname removes Claude Code auto-name transcript files that are not tied to a clyde session.
func PruneAutoname(
	ctx context.Context,
	store session.Store,
	log *slog.Logger,
	out io.Writer,
	opts Options,
) (Result, error) {
	if log == nil {
		log = slog.Default()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	log.Info("prune.autoname.started", "component", "prune", "dry_run", opts.DryRun)

	minAge := opts.AutonameMinAge
	if minAge == 0 {
		minAge = defaultAutonameMinAge
	}

	home, err := os.UserHomeDir()
	if err != nil {
		log.Error("prune.autoname.home_failed", "component", "prune", "err", err)
		return Result{}, err
	}
	projectsDir := config.ClaudeProjectsRoot(home)
	results, err := session.ScanProjects(projectsDir)
	if err != nil {
		log.Error("prune.autoname.scan_failed", "component", "prune", "err", err)
		return Result{}, err
	}

	knownPaths, err := buildKnownTranscriptPaths(store)
	if err != nil {
		log.Error("prune.autoname.known_paths_failed", "component", "prune", "err", err)
		return Result{}, err
	}

	cutoff := time.Now().Add(-minAge)
	var matches []session.DiscoveryResult
	for _, result := range results {
		if !result.IsAutoName {
			continue
		}
		if knownPaths[result.PrimaryArtifactPath()] {
			continue
		}
		fileInfo, statErr := os.Stat(result.PrimaryArtifactPath())
		if statErr != nil {
			continue
		}
		if !fileInfo.ModTime().Before(cutoff) {
			continue
		}
		matches = append(matches, result)
		log.Debug("prune.autoname.candidate",
			"component", "prune",
			"transcript", result.PrimaryArtifactPath(),
		)
	}

	if len(matches) == 0 {
		_, _ = fmt.Fprintln(out, "No auto-name transcripts to prune.")
		log.Info("prune.autoname.complete", "component", "prune", "considered", 0, "pruned", 0)
		return Result{Considered: 0, Pruned: 0}, nil
	}

	_, _ = fmt.Fprintf(out, "Found %d auto-name transcript(s):\n", len(matches))
	for _, match := range matches {
		_, _ = fmt.Fprintf(out, "  %s\n", match.PrimaryArtifactPath())
	}

	if opts.DryRun {
		_, _ = fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
		log.Info("prune.autoname.complete", "component", "prune", "considered", len(matches), "pruned", 0, "dry_run", true)
		return Result{Considered: len(matches), Pruned: 0}, nil
	}

	var failures []DeleteFailure
	pruned := 0
	for _, match := range matches {
		log.Debug("prune.autoname.removing", "component", "prune", "transcript", match.PrimaryArtifactPath())
		if err := os.Remove(match.PrimaryArtifactPath()); err != nil {
			_, _ = fmt.Fprintf(out, "  FAIL %s: %v\n", match.PrimaryArtifactPath(), err)
			log.Error("prune.autoname.remove_failed", "component", "prune", "transcript", match.PrimaryArtifactPath(), "err", err)
			failures = append(failures, DeleteFailure{Target: match.PrimaryArtifactPath(), Err: err})
			continue
		}
		pruned++
		log.Debug("prune.autoname.deleted", "component", "prune", "transcript", match.PrimaryArtifactPath())
	}

	_, _ = fmt.Fprintf(out, "\nDeleted %d of %d transcripts.\n", pruned, len(matches))
	log.Info("prune.autoname.complete",
		"component", "prune",
		"considered", len(matches),
		"pruned", pruned,
		"failures", len(failures),
	)

	return Result{
		Considered: len(matches),
		Pruned:     pruned,
		Failures:   failures,
	}, nil
}
