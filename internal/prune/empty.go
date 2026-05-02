package prune

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"goodkind.io/clyde/internal/session"
)

const (
	emptyRequireNoRealAssistant = true
	emptyIncludeNilTranscript   = true
)

// findEmptySessions walks the session store and returns sessions that look abandoned per settings.
func findEmptySessions(store session.Store, settings EmptySettings) ([]*session.Session, []string, error) {
	all, err := store.List()
	if err != nil {
		return nil, nil, err
	}
	var hits []*session.Session
	var reasons []string
	cutoff := time.Now().Add(-settings.MinAge)

	for _, sess := range all {
		transcriptPath := sess.Metadata.ProviderTranscriptPath()
		if transcriptPath == "" || !fileExists(transcriptPath) {
			if emptyIncludeNilTranscript {
				hits = append(hits, sess)
				reasons = append(reasons, "no transcript")
			}
			continue
		}
		info, err := os.Stat(transcriptPath)
		if err != nil {
			continue
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}

		lines, realAssistant := countTranscript(transcriptPath)
		if lines >= settings.MaxLines {
			continue
		}
		if emptyRequireNoRealAssistant && realAssistant > 0 {
			continue
		}
		hits = append(hits, sess)
		reasons = append(reasons,
			fmt.Sprintf("lines=%d asst=%d age=%s",
				lines, realAssistant,
				shortDuration(time.Since(info.ModTime()))))
	}
	return hits, reasons, nil
}

func countTranscript(path string) (lines, realAssistant int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		lines++
		var envelope struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			continue
		}
		if envelope.Type == "assistant" && envelope.Message.Model != "" && envelope.Message.Model != "<synthetic>" {
			realAssistant++
		}
	}
	return lines, realAssistant
}

// PruneEmpty deletes sessions that look abandoned (no real assistant, stale or missing transcript).
func PruneEmpty(
	ctx context.Context,
	store session.Store,
	log *slog.Logger,
	out io.Writer,
	opts Options,
) (Result, error) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("prune.empty.started", "component", "prune", "dry_run", opts.DryRun)

	settings := opts.Empty
	if settings.MaxLines == 0 {
		settings.MaxLines = DefaultEmptySettings().MaxLines
	}
	if settings.MinAge == 0 {
		settings.MinAge = DefaultEmptySettings().MinAge
	}

	hits, reasons, err := findEmptySessions(store, settings)
	if err != nil {
		log.Error("prune.empty.list_failed", "component", "prune", "err", err)
		return Result{}, err
	}

	if len(hits) == 0 {
		_, _ = fmt.Fprintln(out, "No empty sessions found.")
		log.Info("prune.empty.complete", "component", "prune", "considered", 0, "pruned", 0)
		return Result{Considered: 0, Pruned: 0}, nil
	}

	for index, sess := range hits {
		log.Debug("prune.empty.candidate",
			"component", "prune",
			"session", sess.Name,
			"reason", reasons[index],
		)
	}

	_, _ = fmt.Fprintf(out, "Found %d empty session(s):\n", len(hits))
	for index, sess := range hits {
		_, _ = fmt.Fprintf(out, "  %-32s  %s\n", sess.Name, reasons[index])
	}

	if opts.DryRun {
		_, _ = fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
		log.Info("prune.empty.complete", "component", "prune", "considered", len(hits), "pruned", 0, "dry_run", true)
		return Result{Considered: len(hits), Pruned: 0}, nil
	}

	if !opts.SkipConfirm {
		if opts.Input == nil {
			return Result{}, fmt.Errorf("prune: Input is required when confirmation is not skipped")
		}
		_, _ = fmt.Fprintf(out, "\nDelete these %d sessions? [y/N]: ", len(hits))
		var answer string
		_, _ = fmt.Fscanln(opts.Input, &answer)
		if !strings.EqualFold(strings.TrimSpace(answer), "y") && !strings.EqualFold(strings.TrimSpace(answer), "yes") {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			log.Info("prune.empty.cancelled", "component", "prune", "considered", len(hits))
			return Result{Considered: len(hits), Pruned: 0}, nil
		}
	}

	var failures []DeleteFailure
	pruned := 0
	for _, sess := range hits {
		log.Debug("prune.empty.deleting", "component", "prune", "session", sess.Name)
		if err := deleteTrackedSession(ctx, log, out, sess, store); err != nil {
			_, _ = fmt.Fprintf(out, "  FAIL %s: %v\n", sess.Name, err)
			log.Error("prune.empty.delete_failed", "component", "prune", "session", sess.Name, "err", err)
			failures = append(failures, DeleteFailure{Target: sess.Name, Err: err})
			continue
		}
		pruned++
		log.Debug("prune.empty.deleted", "component", "prune", "session", sess.Name)
	}

	_, _ = fmt.Fprintf(out, "\nDeleted %d of %d empty sessions.\n", pruned, len(hits))
	log.Info("prune.empty.complete",
		"component", "prune",
		"considered", len(hits),
		"pruned", pruned,
		"failures", len(failures),
	)

	return Result{
		Considered: len(hits),
		Pruned:     pruned,
		Failures:   failures,
	}, nil
}
