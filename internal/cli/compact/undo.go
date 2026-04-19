package compact

import (
	"fmt"
	"io"
	"log/slog"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
)

func runUndo(out io.Writer, sess *session.Session, path string) error {
	slog.Info("cli.compact.undo.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
	)
	entry, err := compactengine.Undo(sess.Metadata.SessionID, path)
	if err != nil {
		slog.Error("cli.compact.undo.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			"transcript", path,
			slog.Any("err", err),
		)
		return err
	}
	_, _ = fmt.Fprintf(out, "undid %s apply (boundary=%s, synthetic=%s); transcript truncated to %d bytes\n",
		entry.Timestamp.UTC().Format("2006-01-02 15:04:05Z"),
		shortUUID(entry.BoundaryUUID), shortUUID(entry.SyntheticUUID), entry.PreApplyOffset)
	slog.Info("cli.compact.undo.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
		"pre_apply_offset", entry.PreApplyOffset,
	)
	return nil
}
