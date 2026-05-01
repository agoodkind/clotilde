package compact

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/claude"
	"goodkind.io/clyde/internal/cli"
	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/sessionctx"
)

// layerCounter adapts sessionctx.Layer to the planner's Counter
// interface. Every count_tokens call from the target loop goes
// through the unified layer so future backend swaps (local
// tokenizer, cached responses, etc.) need to change only one place.
type layerCounter struct {
	layer sessionctx.Layer
	model string
}

// CountSyntheticUser forwards to Layer.Count with the layer's default
// model. The planner never overrides the model per call, so
// CountOptions stays empty.
func (c *layerCounter) CountSyntheticUser(ctx context.Context, contentArray []compactengine.OutputBlock) (int, error) {
	return c.layer.Count(ctx, contentArray, sessionctx.CountOptions{Model: c.model})
}

func mergeTypeFlag(s *compactengine.Strippers, csv string) error {
	if csv == "" {
		return nil
	}
	for raw := range strings.SplitSeq(csv, ",") {
		raw = strings.TrimSpace(raw)
		switch raw {
		case "":
			continue
		case "all":
			s.SetAll()
		case "tools":
			s.Tools = true
		case "thinking":
			s.Thinking = true
		case "images":
			s.Images = true
		case "chat":
			s.Chat = true
		default:
			return fmt.Errorf("unknown --type entry %q (expected tools|thinking|images|chat|all)", raw)
		}
	}
	return nil
}

func resolveModelLikeTUI(
	store session.Store,
	sess *session.Session,
	fallback string,
) (countModel string, displayModel string, source string) {
	if sess != nil && sess.Metadata.TranscriptPath != "" {
		rawModel, _ := claude.ExtractRawModelAndLastTime(sess.Metadata.TranscriptPath)
		rawModel = strings.TrimSpace(rawModel)
		if rawModel != "" {
			return rawModel, claude.FormatModelFamily(rawModel), "transcript"
		}
	}
	if store != nil && sess != nil && strings.TrimSpace(sess.Name) != "" {
		settings, err := store.LoadSettings(sess.Name)
		if err == nil && settings != nil && strings.TrimSpace(settings.Model) != "" {
			settingsModel := strings.TrimSpace(settings.Model)
			return settingsModel, settingsModel, "settings"
		}
	}
	return fallback, fallback, "fallback"
}

func runCompact(cmd *cobra.Command, f *cli.Factory, args []string) error {
	name := args[0]
	slog.Info("cli.compact.invoked", "session", name)

	if _, err := f.Config(); err != nil {
		slog.Error("cli.compact.config_failed", "session", name, "err", err)
		return err
	}

	out := f.IOStreams.Out
	store, err := f.Store()
	if err != nil {
		slog.Error("cli.compact.store_failed", "session", name, "err", err)
		return err
	}
	sess, err := store.Resolve(name)
	if err != nil {
		slog.Error("cli.compact.resolve_failed", "session", name, "err", err)
		return err
	}
	if sess == nil {
		slog.Warn("cli.compact.session_not_found", "session", name)
		return fmt.Errorf("session %q not found", name)
	}
	path := sess.Metadata.TranscriptPath
	if path == "" {
		slog.Warn("cli.compact.no_transcript_path", "session", name, "session_id", sess.Metadata.SessionID)
		return fmt.Errorf("session %q has no transcript path", name)
	}
	if _, err := os.Stat(path); err != nil {
		slog.Error("cli.compact.transcript_stat_failed", "session", name, "transcript", path, "err", err)
		return fmt.Errorf("transcript not found: %s", path)
	}

	if listB, _ := cmd.Flags().GetBool("list-backups"); listB {
		return runListBackups(out, sess)
	}
	if undo, _ := cmd.Flags().GetBool("undo"); undo {
		return runUndo(out, sess, path)
	}
	if cal, _ := cmd.Flags().GetInt("calibrate"); cal > 0 {
		model, _ := cmd.Flags().GetString("model")
		return runCalibrate(out, sess, cal, model)
	}
	if auto, _ := cmd.Flags().GetBool("auto-calibrate"); auto {
		model, _ := cmd.Flags().GetString("model")
		return runAutoCalibrate(cmd.Context(), out, sess, model)
	}

	// Target can come from either the positional [target] arg or the
	// --target flag. The flag wins when both are present so scripts
	// can be position-independent.
	target := 0
	targetFlag, _ := cmd.Flags().GetString("target")
	targetRaw := strings.TrimSpace(targetFlag)
	if targetRaw == "" && len(args) == 2 {
		targetRaw = args[1]
	}
	if targetRaw != "" {
		n, perr := ParseTokenCount(targetRaw)
		if perr != nil {
			slog.Warn("cli.compact.invalid_target", "session", name, "target_raw", targetRaw, slog.Any("err", perr))
			return fmt.Errorf("invalid target %q: %w", targetRaw, perr)
		}
		target = n
	}

	flagTools, _ := cmd.Flags().GetBool("tools")
	flagThinking, _ := cmd.Flags().GetBool("thinking")
	flagImages, _ := cmd.Flags().GetBool("images")
	flagChat, _ := cmd.Flags().GetBool("chat")
	flagAll, _ := cmd.Flags().GetBool("all")
	flagTypes, _ := cmd.Flags().GetString("type")
	apply, _ := cmd.Flags().GetBool("apply")
	force, _ := cmd.Flags().GetBool("force")
	reserved, _ := cmd.Flags().GetInt("reserved")
	model, _ := cmd.Flags().GetString("model")
	modelDisplay := model
	modelExplicit := cmd.Flags().Changed("model")
	showPasses, _ := cmd.Flags().GetBool("show-passes")
	if !modelExplicit {
		resolvedModel, resolvedDisplayModel, resolvedSource := resolveModelLikeTUI(store, sess, model)
		model = resolvedModel
		modelDisplay = resolvedDisplayModel
		slog.Info("cli.compact.model_resolved",
			"session", name,
			"model_count", model,
			"model_display", modelDisplay,
			"source", resolvedSource,
		)
	}

	strippers := compactengine.Strippers{
		Tools:    flagTools,
		Thinking: flagThinking,
		Images:   flagImages,
		Chat:     flagChat,
	}
	if flagAll {
		strippers.SetAll()
	}
	if err := mergeTypeFlag(&strippers, flagTypes); err != nil {
		slog.Warn("cli.compact.type_flag_invalid", "session", name, "err", err)
		return err
	}
	if !strippers.Any() && target == 0 {
		refresh, _ := cmd.Flags().GetBool("refresh")
		return runMetricsDashboard(cmd.Context(), out, sess, path, refresh)
	}
	if target > 0 && !strippers.Any() {
		strippers.SetAll()
	}
	if strippers.Chat && target == 0 {
		slog.Warn("cli.compact.chat_requires_target", "session", name)
		return fmt.Errorf("--chat requires a positive target token count")
	}
	if target > 0 {
		mode := ModePreview
		if apply {
			mode = ModeApply
		}
		isTTY := isTerminal(out)
		summarize, _ := cmd.Flags().GetBool("summarize")
		_, _ = fmt.Fprintf(out, "starting compact %s for %s; gathering startup stats...\n",
			strings.ToLower(mode.Label()), sess.Name)
		daemonErr := runCompactViaDaemon(cmd.Context(), out, compactDaemonRunInput{
			SessionName:    sess.Name,
			Mode:           mode,
			Target:         target,
			Reserved:       reserved,
			Model:          model,
			ModelExplicit:  modelExplicit,
			Strippers:      strippers,
			Summarize:      summarize,
			Force:          force,
			ShowPasses:     showPasses && !isTTY,
			IsTTY:          isTTY,
			TranscriptPath: path,
		})
		if daemonErr == nil {
			slog.Info("cli.compact.completed_via_daemon", "session", name, "mode", mode.Label())
			return nil
		}
		slog.Error("cli.compact.daemon_path_failed", "session", name, slog.Any("err", daemonErr))
		return daemonErr
	}

	slice, err := compactengine.LoadSlice(path)
	if err != nil {
		slog.Error("cli.compact.load_slice_failed", "session", name, "transcript", path, "err", err)
		return err
	}

	staticOverhead := 0
	if target > 0 {
		cal, ok, calErr := compactengine.LoadCalibration(sess.Metadata.SessionID)
		if calErr != nil {
			slog.Error("cli.compact.calibration_load_failed", "session", name, "session_id", sess.Metadata.SessionID, slog.Any("err", calErr))
			return calErr
		}
		if !ok {
			// Auto-probe on miss. Spawning claude with /context is the
			// only way to learn the session's static overhead and we
			// know how to do it transparently, so running it
			// automatically is strictly better than refusing the
			// command and asking the user to do the probe themselves.
			slog.Info("cli.compact.calibration_auto_probe", "session", name, "session_id", sess.Metadata.SessionID)
			_, _ = fmt.Fprintf(out, "no calibration on file; probing claude /context for static overhead (30-60 seconds)...\n")
			if err := runAutoCalibrate(cmd.Context(), out, sess, model); err != nil {
				slog.Error("cli.compact.calibration_auto_probe_failed", "session", name, "session_id", sess.Metadata.SessionID, "err", err)
				return err
			}
			cal, ok, calErr = compactengine.LoadCalibration(sess.Metadata.SessionID)
			if calErr != nil || !ok {
				slog.Error("cli.compact.calibration_post_probe_missing", "session", name, "session_id", sess.Metadata.SessionID, slog.Any("err", calErr))
				return fmt.Errorf("auto-probe finished but calibration still missing for %q", name)
			}
		}
		staticOverhead = cal.StaticOverhead
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()

	mode := ModePreview
	if apply {
		mode = ModeApply
	}
	if target > 0 {
		// Emit immediate feedback before any potentially slow context
		// usage probe or token-count setup work.
		_, _ = fmt.Fprintf(out, "starting compact %s for %s; gathering startup stats...\n",
			strings.ToLower(mode.Label()), sess.Name)
	}

	// Phase 1: upfront info panel. Every number we can show without
	// touching the network goes here so the user sees the full picture
	// before the long calculation begins.
	var upfrontStats UpfrontStats
	if target > 0 {
		thinking, images, toolPairs, chatTurns := categoryCounts(slice)
		currentTotal, maxTokens := 0, 0
		calibDate := ""
		layer := sessionctx.NewDefault(sess, model, "")
		if u, uerr := layer.Usage(ctx, sessionctx.UsageOptions{MaxAge: 24 * time.Hour}); uerr == nil {
			currentTotal = u.TotalTokens
			maxTokens = u.MaxTokens
		}
		if cal, ok, _ := compactengine.LoadCalibration(sess.Metadata.SessionID); ok {
			calibDate = cal.CapturedAt.UTC().Format("2006-01-02")
		}
		upfrontStats = UpfrontStats{
			SessionName:   sess.Name,
			SessionID:     sess.Metadata.SessionID,
			Model:         modelDisplay,
			Mode:          mode,
			CurrentTotal:  currentTotal,
			MaxTokens:     maxTokens,
			Target:        target,
			StaticFloor:   staticOverhead,
			Reserved:      reserved,
			Thinking:      thinking,
			Images:        images,
			ToolPairs:     toolPairs,
			ChatTurns:     chatTurns,
			StrippersText: strippersDescribe(strippers),
			TargetDate:    calibDate,
		}
		if !isTerminal(out) {
			RenderUpfrontPanel(out, upfrontStats)
		}
	}

	var counter compactengine.Counter
	if target > 0 {
		key, keyErr := compactengine.AnthropicAPIKey()
		if keyErr != nil {
			slog.Error("cli.compact.api_key_failed", "session", name, slog.Any("err", keyErr))
			return keyErr
		}
		layer := sessionctx.NewDefault(sess, model, key)
		counter = &layerCounter{layer: layer, model: model}
	}

	// Phase 2: rolling spinner during the target loop. Mode banner
	// stays visible on every frame so destructive runs cannot be
	// confused for preview runs.
	slog.Info("cli.compact.preview.run_plan.started", "session", name, "target", target, "mode", mode.Label())
	isTTY := isTerminal(out)
	var progress *progressView
	var onIter func(compactengine.IterationRecord)
	if target > 0 {
		progress = newProgressView(out, target, mode, isTTY, upfrontStats)
		onIter = progress.Update
	}
	planRes, err := compactengine.RunPlan(ctx, compactengine.PlanInput{
		Slice:          slice,
		Strippers:      strippers,
		Target:         target,
		StaticOverhead: staticOverhead,
		Reserved:       reserved,
		Counter:        counter,
		OnIteration:    onIter,
	})
	if progress != nil {
		progress.Finish()
	}
	if err != nil {
		slog.Error("cli.compact.preview.run_plan.failed", "session", name, "err", err)
		return err
	}
	slog.Info("cli.compact.preview.run_plan.completed", "session", name, "hit_target", planRes.HitTarget)

	// Phase 3: result box. Keep TTY focused on the single live pane.
	// show-passes remains available for non-TTY logs and debugging.
	if target > 0 && isTTY && progress != nil {
		progress.Complete(planRes, staticOverhead, reserved, apply, path)
	}
	if target > 0 && showPasses && !isTTY {
		RenderIterationLog(out, planRes.Iterations)
	}
	if target == 0 {
		RenderNoTarget(out, mode, sess.Name, strippers, planRes, len(planRes.BoundaryTail), len(slice.PostBoundary))
	} else if !apply && !isTTY {
		RenderFinalPreview(out, planRes, target, staticOverhead, reserved)
	}

	if !apply {
		slog.Info("cli.compact.preview.completed", "session", name, "applied", false)
		return nil
	}

	// Before mutating the transcript, optionally ask claude -p for a
	// recap of the dropped portion and inject it into the synthetic
	// header. The recap helps the continuing agent pick up work the
	// deterministic header cannot represent (intents, decisions, open
	// threads).
	if summarize, _ := cmd.Flags().GetBool("summarize"); summarize {
		_, _ = fmt.Fprintln(out, "summarizing dropped content via claude -p (30-60s)...")
		summary, sumErr := compactengine.SummarizeDropped(ctx, slice, planRes.Options, compactengine.SummarizeOptions{
			Model: model,
		})
		if sumErr != nil {
			slog.Warn("cli.compact.summarize_failed_continuing", "session", name, slog.Any("err", sumErr))
			_, _ = fmt.Fprintf(out, "summary failed (%v); applying without summary\n", sumErr)
		} else if summary != "" {
			planRes.Options.Summary = summary
			planRes.BoundaryTail = compactengine.Synthesize(slice, planRes.Options)
			slog.Info("cli.compact.summarize_injected", "session", name, "summary_bytes", len(summary))
		}
	}

	if err := runApply(out, sess, slice, strippers, target, planRes, force); err != nil {
		return err
	}
	if !isTTY {
		RenderFinalApply(out, planRes, target, staticOverhead, reserved, path)
	}
	return nil
}
