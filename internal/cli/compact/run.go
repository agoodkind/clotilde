package compact

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/internal/cli"
	compactengine "goodkind.io/clyde/internal/compact"
)

func mergeTypeFlag(s *compactengine.Strippers, csv string) error {
	if csv == "" {
		return nil
	}
	for _, raw := range strings.Split(csv, ",") {
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

func runCompact(cmd *cobra.Command, f *cli.Factory, args []string) error {
	name := args[0]
	slog.Info("cli.compact.invoked", "session", name)

	if _, err := f.Config(); err != nil {
		slog.Error("cli.compact.config_failed", "session", name, slog.Any("err", err))
		return err
	}

	out := f.IOStreams.Out
	store, err := f.Store()
	if err != nil {
		slog.Error("cli.compact.store_failed", "session", name, slog.Any("err", err))
		return err
	}
	sess, err := store.Resolve(name)
	if err != nil {
		slog.Error("cli.compact.resolve_failed", "session", name, slog.Any("err", err))
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
		slog.Error("cli.compact.transcript_stat_failed", "session", name, "transcript", path, slog.Any("err", err))
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

	target := 0
	if len(args) == 2 {
		n, err := ParseTokenCount(args[1])
		if err != nil {
			slog.Warn("cli.compact.invalid_target", "session", name, "target_raw", args[1], slog.Any("err", err))
			return fmt.Errorf("invalid target %q: %w", args[1], err)
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
		slog.Warn("cli.compact.type_flag_invalid", "session", name, slog.Any("err", err))
		return err
	}
	if !strippers.Any() && target == 0 {
		return runMetricsDashboard(out, sess, path)
	}
	if target > 0 && !strippers.Any() {
		strippers.SetAll()
	}
	if strippers.Chat && target == 0 {
		slog.Warn("cli.compact.chat_requires_target", "session", name)
		return fmt.Errorf("--chat requires a positive target token count")
	}

	slice, err := compactengine.LoadSlice(path)
	if err != nil {
		slog.Error("cli.compact.load_slice_failed", "session", name, "transcript", path, slog.Any("err", err))
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
			slog.Warn("cli.compact.calibration_missing", "session", name, "session_id", sess.Metadata.SessionID)
			return fmt.Errorf("session %q has no calibration. Run a real /context against this session, then:\n  clyde compact %s --calibrate=<static_overhead_from_context>", name, name)
		}
		staticOverhead = cal.StaticOverhead
	}

	var counter *compactengine.TokenCounter
	if target > 0 {
		key, err := compactengine.AnthropicAPIKey()
		if err != nil {
			slog.Error("cli.compact.api_key_failed", "session", name, slog.Any("err", err))
			return err
		}
		counter = compactengine.NewTokenCounter(key, model)
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Minute)
	defer cancel()

	slog.Info("cli.compact.preview.run_plan.started", "session", name, "target", target)
	planRes, err := compactengine.RunPlan(ctx, compactengine.PlanInput{
		Slice:          slice,
		Strippers:      strippers,
		Target:         target,
		StaticOverhead: staticOverhead,
		Reserved:       reserved,
		Counter:        counter,
		Out:            out,
	})
	if err != nil {
		slog.Error("cli.compact.preview.run_plan.failed", "session", name, slog.Any("err", err))
		return err
	}
	slog.Info("cli.compact.preview.run_plan.completed", "session", name, "hit_target", planRes.HitTarget)

	runPlanPreview(out, sess, slice, target, staticOverhead, reserved, model, strippers, planRes)

	if !apply {
		_, _ = fmt.Fprintln(out, "\n(preview only, pass --apply to mutate)")
		slog.Info("cli.compact.preview.completed", "session", name, "applied", false)
		return nil
	}

	return runApply(out, sess, slice, strippers, target, planRes, force)
}
