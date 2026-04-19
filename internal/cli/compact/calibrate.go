package compact

import (
	"fmt"
	"io"
	"log/slog"
	"time"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
)

func runCalibrate(out io.Writer, sess *session.Session, n int, model string) error {
	slog.Info("cli.compact.calibrate.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"static_overhead", n,
		"model", model,
	)
	cal := compactengine.Calibration{
		StaticOverhead: n,
		CapturedAt:     time.Now().UTC(),
		Model:          model,
	}
	if err := compactengine.SaveCalibration(sess.Metadata.SessionID, cal); err != nil {
		slog.Error("cli.compact.calibrate.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			slog.Any("err", err),
		)
		return err
	}
	_, _ = fmt.Fprintf(out, "calibrated session %s: static_overhead = %s\n", sess.Name, humanInt(n))
	slog.Info("cli.compact.calibrate.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"static_overhead", n,
	)
	return nil
}
