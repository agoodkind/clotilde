package compact

import (
	"fmt"
	"io"
	"strings"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
)

func runListBackups(out io.Writer, sess *session.Session) error {
	cliCompactLog.Logger().Info("cli.compact.ledger.started",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
	)
	entries, err := compactengine.ReadLedger(sess.Metadata.ProviderSessionID())
	if err != nil {
		cliCompactLog.Logger().Error("cli.compact.ledger.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"err", err,
		)
		return err
	}
	if len(entries) == 0 {
		_, _ = fmt.Fprintln(out, "(no ledger entries)")
		cliCompactLog.Logger().Info("cli.compact.ledger.completed",
			"session", sess.Name,
			"session_id", sess.Metadata.ProviderSessionID(),
			"entries", 0,
		)
		return nil
	}
	for _, e := range compactengine.SortLedger(entries) {
		_, _ = fmt.Fprintf(out, "%s  op=%s  target=%s  strips=%s  pre=%d  snap=%s\n",
			e.Timestamp.UTC().Format("2006-01-02 15:04:05Z"),
			e.Op,
			humanInt(e.Target),
			strings.Join(e.Strips, ","),
			e.PreApplyOffset,
			e.SnapshotPath)
	}
	cliCompactLog.Logger().Info("cli.compact.ledger.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
		"entries", len(entries),
	)
	return nil
}
