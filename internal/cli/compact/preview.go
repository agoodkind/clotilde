package compact

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/util"
)

func runMetricsDashboard(out io.Writer, sess *session.Session, path string) error {
	slog.Info("cli.compact.preview.metrics.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
	)
	slice, err := compactengine.LoadSlice(path)
	if err != nil {
		slog.Error("cli.compact.preview.metrics.failed",
			"session", sess.Name,
			"transcript", path,
			slog.Any("err", err),
		)
		return err
	}
	stat, err := os.Stat(path)
	if err != nil {
		slog.Error("cli.compact.preview.metrics.failed",
			"session", sess.Name,
			"transcript", path,
			slog.Any("err", err),
		)
		return err
	}
	_, _ = fmt.Fprintln(out, "session     "+sess.Name)
	_, _ = fmt.Fprintln(out, "uuid        "+sess.Metadata.SessionID)
	_, _ = fmt.Fprintf(out, "file        %s   (%d lines, %s)\n", path, len(slice.AllEntries), util.FormatSize(stat.Size()))

	if cal, ok, _ := compactengine.LoadCalibration(sess.Metadata.SessionID); ok {
		_, _ = fmt.Fprintf(out, "calibration static_overhead = %s  (captured %s)\n",
			humanInt(cal.StaticOverhead), cal.CapturedAt.UTC().Format("2006-01-02"))
	} else {
		_, _ = fmt.Fprintf(out, "calibration NOT CALIBRATED  (run: clyde compact %s --calibrate=N)\n", sess.Name)
	}
	_, _ = fmt.Fprintf(out, "reserved    %s   (default, assumes autocompact on)\n", humanInt(DefaultReservedBuffer))
	_, _ = fmt.Fprintln(out)

	if slice.BoundaryLine >= 0 {
		_, _ = fmt.Fprintf(out, "boundary    line %d  (uuid %s, %s)\n",
			slice.BoundaryLine+1, shortUUID(slice.BoundaryUUID), slice.BoundaryTime.UTC().Format(time.RFC3339))
	} else {
		_, _ = fmt.Fprintln(out, "boundary    none")
	}
	_, _ = fmt.Fprintf(out, "            %d entries since boundary\n", len(slice.PostBoundary))

	thinking, images, toolPairs, chatTurns := categoryCounts(slice)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "tail")
	_, _ = fmt.Fprintf(out, "  thinking blocks   %d   ~%s tok (rough)\n", thinking, humanInt(thinking*200))
	_, _ = fmt.Fprintf(out, "  image blocks      %d   ~%s tok (rough)\n", images, humanInt(images*150))
	_, _ = fmt.Fprintf(out, "  tool_use/result   %d pairs   ~%s tok (rough)\n", toolPairs, humanInt(toolPairs*800))
	_, _ = fmt.Fprintf(out, "  chat turns        %d   ~%s tok (rough)\n", chatTurns, humanInt(chatTurns*120))
	slog.Info("cli.compact.preview.metrics.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
	)
	return nil
}

func categoryCounts(slice *compactengine.Slice) (thinking, images, toolPairs, chatTurns int) {
	for _, e := range slice.PostBoundary {
		for _, b := range e.Content {
			switch b.Type {
			case "thinking", "redacted_thinking":
				thinking++
			case "image":
				images++
			}
		}
		if e.Type == "user" || e.Type == "assistant" {
			chatTurns++
		}
	}
	toolPairs = len(slice.PairIndex)
	return
}

// runPlanPreview prints the iteration log and before/after summary after RunPlan.
func runPlanPreview(
	out io.Writer,
	sess *session.Session,
	slice *compactengine.Slice,
	target, staticOverhead, reserved int,
	model string,
	s compactengine.Strippers,
	res *compactengine.PlanResult,
) {
	slog.Info("cli.compact.preview.plan.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"target", target,
		"model", model,
	)
	_, _ = fmt.Fprintln(out, "session     "+sess.Name)
	_, _ = fmt.Fprintln(out, "model       "+model)
	if target > 0 {
		_, _ = fmt.Fprintf(out, "static      %s   (calibrated)\n", humanInt(staticOverhead))
		_, _ = fmt.Fprintf(out, "reserved    %s\n", humanInt(reserved))
		_, _ = fmt.Fprintf(out, "target      %s\n", humanInt(target))
		_, _ = fmt.Fprintln(out)
		for i, it := range res.Iterations {
			over := it.Delta
			tag := "OK"
			if over > 0 {
				tag = fmt.Sprintf("+%s over", humanInt(over))
			} else {
				tag = fmt.Sprintf("-%s OK", humanInt(-over))
			}
			_, _ = fmt.Fprintf(out, "iter %-2d %-44s tail=%s  ctx=%s  %s\n",
				i, it.Step, humanInt(it.TailTokens), humanInt(it.CtxTotal), tag)
		}
		_, _ = fmt.Fprintln(out)
		_, _ = fmt.Fprintf(out, "result  tail %s -> %s   ctx %s -> %s   %s\n",
			humanInt(res.BaselineTail), humanInt(res.FinalTail),
			humanInt(staticOverhead+res.BaselineTail+reserved),
			humanInt(staticOverhead+res.FinalTail+reserved),
			func() string {
				if res.HitTarget {
					return "(under target)"
				}
				return "(STILL OVER TARGET)"
			}())
		slog.Info("cli.compact.preview.plan.completed",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"hit_target", res.HitTarget,
			"baseline_tail", res.BaselineTail,
			"final_tail", res.FinalTail,
		)
		return
	}
	_, _ = fmt.Fprintln(out, "no target; strippers applied at most-aggressive setting")
	_, _ = fmt.Fprintln(out, "selected: "+strippersDescribe(s))
	_, _ = fmt.Fprintf(out, "synthesized content blocks: %d\n", len(res.BoundaryTail))
	_, _ = fmt.Fprintf(out, "post-boundary entries:      %d\n", len(slice.PostBoundary))
	slog.Info("cli.compact.preview.plan.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"boundary_blocks", len(res.BoundaryTail),
	)
}

func strippersDescribe(s compactengine.Strippers) string {
	var parts []string
	if s.Thinking {
		parts = append(parts, "thinking")
	}
	if s.Images {
		parts = append(parts, "images")
	}
	if s.Tools {
		parts = append(parts, "tools")
	}
	if s.Chat {
		parts = append(parts, "chat")
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, ",")
}
