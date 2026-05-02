package compact

import (
	"io"
	"log/slog"
	"os"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
)

func runUndo(out io.Writer, sess *session.Session, path string) error {
	cliCompactLog.Logger().Info("cli.compact.undo.started",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"transcript", path,
	)
	entry, err := compactengine.Undo(sess.Metadata.ProviderSessionID(), path)
	if err != nil {
		cliCompactLog.Logger().Error("cli.compact.undo.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"transcript", path,
			"err", err,
		)
		return err
	}
	ledgerPath, ledgerErr := compactengine.LedgerPath(sess.Metadata.ProviderSessionID())
	if ledgerErr != nil {
		cliCompactLog.Logger().Warn("cli.compact.undo.ledger_path_failed", "session", sess.Name, slog.Any("err", ledgerErr))
	}
	stat, statErr := os.Stat(path)
	postBytes := int64(-1)
	if statErr == nil {
		postBytes = stat.Size()
	}
	preBytes := entry.PreApplyOffset
	RenderUndoResult(out, sess.Name, sess.Metadata.ProviderSessionID(), path, ledgerPath, entry, preBytes, postBytes)
	cliCompactLog.Logger().Info("cli.compact.undo.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"transcript", path,
		"pre_apply_offset", entry.PreApplyOffset,
	)
	return nil
}
