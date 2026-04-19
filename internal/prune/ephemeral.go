package prune

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"goodkind.io/clyde/internal/session"
)

// isEphemeralWorkspace matches workspace roots that look like temp or test scratch paths.
func isEphemeralWorkspace(workspaceRoot string) bool {
	if workspaceRoot == "" {
		return false
	}
	prefixes := []string{
		"/private/var/folders/",
		"/var/folders/",
		"/tmp/",
		"/private/tmp/",
	}
	for _, prefix := range prefixes {
		if strings.HasPrefix(workspaceRoot, prefix) {
			return true
		}
	}
	return strings.Contains(workspaceRoot, "/ginkgo")
}

// PruneEphemeral deletes sessions whose workspace roots look like temp or test scratch paths.
func PruneEphemeral(
	ctx context.Context,
	store session.Store,
	log *slog.Logger,
	out io.Writer,
	opts Options,
) (Result, error) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("prune.ephemeral.started", "component", "prune", "dry_run", opts.DryRun)

	all, err := store.List()
	if err != nil {
		log.Error("prune.ephemeral.list_failed", "component", "prune", slog.Any("err", err))
		return Result{}, fmt.Errorf("listing sessions: %w", err)
	}

	var targets []*session.Session
	for _, sess := range all {
		if isEphemeralWorkspace(sess.Metadata.WorkspaceRoot) {
			targets = append(targets, sess)
			log.Debug("prune.ephemeral.candidate",
				"component", "prune",
				"session", sess.Name,
				"workspace", sess.Metadata.WorkspaceRoot,
			)
		}
	}

	if len(targets) == 0 {
		_, _ = fmt.Fprintln(out, "No ephemeral sessions found.")
		log.Info("prune.ephemeral.complete", "component", "prune", "considered", 0, "pruned", 0)
		return Result{Considered: 0, Pruned: 0}, nil
	}

	_, _ = fmt.Fprintf(out, "Found %d ephemeral session(s):\n", len(targets))
	for _, sess := range targets {
		_, _ = fmt.Fprintf(out, "  %s  %s\n", sess.Name, sess.Metadata.WorkspaceRoot)
	}

	if opts.DryRun {
		_, _ = fmt.Fprintln(out, "\n[dry-run] No deletions performed.")
		log.Info("prune.ephemeral.complete", "component", "prune", "considered", len(targets), "pruned", 0, "dry_run", true)
		return Result{Considered: len(targets), Pruned: 0}, nil
	}

	if !opts.SkipConfirm {
		if opts.Input == nil {
			return Result{}, fmt.Errorf("prune: Input is required when confirmation is not skipped")
		}
		_, _ = fmt.Fprintf(out, "\nDelete these %d sessions? [y/N]: ", len(targets))
		var answer string
		_, _ = fmt.Fscanln(opts.Input, &answer)
		answer = strings.ToLower(strings.TrimSpace(answer))
		if answer != "y" && answer != "yes" {
			_, _ = fmt.Fprintln(out, "Cancelled.")
			log.Info("prune.ephemeral.cancelled", "component", "prune", "considered", len(targets))
			return Result{Considered: len(targets), Pruned: 0}, nil
		}
	}

	var failures []DeleteFailure
	pruned := 0
	for _, sess := range targets {
		log.Info("prune.ephemeral.deleting", "component", "prune", "session", sess.Name)
		if err := deleteTrackedSession(ctx, log, out, sess, store); err != nil {
			_, _ = fmt.Fprintf(out, "  FAILED %s: %v\n", sess.Name, err)
			log.Error("prune.ephemeral.delete_failed", "component", "prune", "session", sess.Name, slog.Any("err", err))
			failures = append(failures, DeleteFailure{Target: sess.Name, Err: err})
			continue
		}
		pruned++
		log.Info("prune.ephemeral.deleted", "component", "prune", "session", sess.Name)
	}

	_, _ = fmt.Fprintf(out, "\nDeleted %d of %d ephemeral sessions.\n", pruned, len(targets))
	log.Info("prune.ephemeral.complete",
		"component", "prune",
		"considered", len(targets),
		"pruned", pruned,
		"failures", len(failures),
	)

	return Result{
		Considered: len(targets),
		Pruned:     pruned,
		Failures:   failures,
	}, nil
}
