package compact

import (
	"io"
	"log/slog"
	"os"

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
			"err", err,
		)
		return err
	}
	ledgerPath, ledgerErr := compactengine.LedgerPath(sess.Metadata.SessionID)
	if ledgerErr != nil {
		slog.Warn("cli.compact.undo.ledger_path_failed", "session", sess.Name, slog.Any("err", ledgerErr))
	}
	stat, statErr := os.Stat(path)
	postBytes := int64(-1)
	if statErr == nil {
		postBytes = stat.Size()
	}
	preBytes := entry.PreApplyOffset
	RenderUndoResult(out, sess.Name, sess.Metadata.SessionID, path, ledgerPath, entry, preBytes, postBytes)
	slog.Info("cli.compact.undo.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"transcript", path,
		"pre_apply_offset", entry.PreApplyOffset,
	)
	return nil
}
