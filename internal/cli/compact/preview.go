package compact

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/sessionctx"
	"goodkind.io/clyde/internal/util"
)

func runMetricsDashboard(ctx context.Context, out io.Writer, sess *session.Session, path string, refresh bool) error {
	slog.Info("cli.compact.preview.metrics.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
		"refresh", refresh,
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
		_, _ = fmt.Fprintf(out, "calibration NOT CALIBRATED  (run: clyde compact %s --auto-calibrate)\n", sess.Name)
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

	// Block counts (informational only). Token numbers come from the
	// probe below; fixed multipliers are gone.
	thinking, images, toolPairs, chatTurns := categoryCounts(slice)
	_, _ = fmt.Fprintln(out)
	_, _ = fmt.Fprintln(out, "tail blocks")
	_, _ = fmt.Fprintf(out, "  thinking blocks   %d\n", thinking)
	_, _ = fmt.Fprintf(out, "  image blocks      %d\n", images)
	_, _ = fmt.Fprintf(out, "  tool_use/result   %d pairs\n", toolPairs)
	_, _ = fmt.Fprintf(out, "  chat turns        %d\n", chatTurns)

	// Probe Claude for the exact /context breakdown. Numbers here are
	// what Claude itself reports, not rough estimates. A 5 minute
	// MaxAge keeps repeat invocations cheap; refresh busts the cache.
	layer := sessionctx.NewDefault(sess, "", "")
	usage, usageErr := layer.Usage(ctx, sessionctx.UsageOptions{
		Refresh: refresh,
		MaxAge:  5 * time.Minute,
	})
	_, _ = fmt.Fprintln(out)
	if usageErr != nil {
		slog.Warn("cli.compact.preview.metrics.usage_failed",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			slog.Any("err", usageErr),
		)
		_, _ = fmt.Fprintf(out, "context     unavailable (%v)\n", usageErr)
		_, _ = fmt.Fprintf(out, "            rerun with --refresh to probe claude /context\n")
	} else {
		_, _ = fmt.Fprintf(out, "context (from claude /context, source=%s, captured=%s)\n",
			usage.Source, usage.CapturedAt.UTC().Format(time.RFC3339))
		for _, cat := range usage.Categories {
			name := cat.Name
			if cat.IsDeferred && !strings.Contains(name, "(deferred)") {
				name += " (deferred)"
			}
			_, _ = fmt.Fprintf(out, "  %-24s %s tok\n", name, humanInt(cat.Tokens))
		}
		_, _ = fmt.Fprintf(out, "  %-24s %s / %s  (%d%%)\n",
			"total", humanInt(usage.TotalTokens), humanInt(usage.MaxTokens), usage.Percentage)
		_, _ = fmt.Fprintf(out, "  %-24s %s tok   (derived: everything except Messages, Compact buffer, Free space)\n",
			"static overhead", humanInt(usage.StaticOverhead()))
	}

	slog.Info("cli.compact.preview.metrics.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
		"usage_available", usageErr == nil,
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
