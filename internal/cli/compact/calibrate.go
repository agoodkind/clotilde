package compact

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	compactengine "goodkind.io/clyde/internal/compact"
	contextusage "goodkind.io/clyde/internal/providers/claude/contextusage"
	"goodkind.io/clyde/internal/session"
)

// runAutoCalibrate probes the live session via the unified
// contextusage.Layer, derives static_overhead from the Usage response,
// and writes the calibration file. The layer routes Usage through
// Claude's get_context_usage control request so the numbers match
// /context exactly. --calibrate=auto always passes Refresh so the
// cache never returns an outdated static_overhead.
func runAutoCalibrate(ctx context.Context, out io.Writer, sess *session.Session, model string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	cliCompactLog.Logger().Info("cli.compact.auto_calibrate.started",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"work_dir", sess.Metadata.WorkDir,
		"model", model,
	)
	_, _ = fmt.Fprintf(out, "probing %s via claude /context (may take 30-60 seconds)...\n", sess.Name)

	layer := contextusage.NewDefault(sess, model, "")
	usage, err := layer.Usage(ctx, contextusage.UsageOptions{Refresh: true})
	if err != nil {
		slog.ErrorContext(ctx, "cli.compact.auto_calibrate.probe_failed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"err", err,
		)
		return fmt.Errorf("auto-calibrate probe: %w", err)
	}

	overhead := usage.StaticOverhead()
	if overhead <= 0 {
		cliCompactLog.Logger().Warn("cli.compact.auto_calibrate.empty_overhead",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"total_tokens", usage.TotalTokens,
			"categories", len(usage.Categories),
		)
		return fmt.Errorf("auto-calibrate: derived static_overhead was zero (total=%d); refusing to save", usage.TotalTokens)
	}
	resolvedModel := model
	if resolvedModel == "" {
		resolvedModel = usage.Model
	}

	cal := compactengine.Calibration{
		StaticOverhead: overhead,
		CapturedAt:     cliCompactClock.Now().UTC(),
		Model:          resolvedModel,
	}
	if err := compactengine.SaveCalibration(sess.Metadata.ProviderSessionID(), cal); err != nil {
		cliCompactLog.Logger().Error("cli.compact.auto_calibrate.save_failed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"err", err,
		)
		return err
	}

	cliCompactLog.Logger().Info("cli.compact.auto_calibrate.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"static_overhead", overhead,
		"total_tokens", usage.TotalTokens,
		"messages_tokens", usage.TailTokens(),
		"model", resolvedModel,
		"source", string(usage.Source),
	)
	_, _ = fmt.Fprintf(out, "auto-calibrated %s: static_overhead = %s (total=%s, derived from %d categories)\n",
		sess.Name, humanInt(overhead), humanInt(usage.TotalTokens), len(usage.Categories))
	return nil
}

func runCalibrate(out io.Writer, sess *session.Session, n int, model string) error {
	cliCompactLog.Logger().Info("cli.compact.calibrate.started",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"static_overhead", n,
		"model", model,
	)
	cal := compactengine.Calibration{
		StaticOverhead: n,
		CapturedAt:     cliCompactClock.Now().UTC(),
		Model:          model,
	}
	if err := compactengine.SaveCalibration(sess.Metadata.ProviderSessionID(), cal); err != nil {
		cliCompactLog.Logger().Error("cli.compact.calibrate.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"err", err,
		)
		return err
	}
	_, _ = fmt.Fprintf(out, "calibrated session %s: static_overhead = %s\n", sess.Name, humanInt(n))
	cliCompactLog.Logger().Info("cli.compact.calibrate.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"static_overhead", n,
	)
	return nil
}
