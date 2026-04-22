package compact

import (
	"fmt"
	"io"
	"log/slog"

	compactengine "goodkind.io/clyde/internal/compact"
	"goodkind.io/clyde/internal/session"
)

func runApply(
	out io.Writer,
	sess *session.Session,
	slice *compactengine.Slice,
	strippers compactengine.Strippers,
	target int,
	planRes *compactengine.PlanResult,
	force bool,
) error {
	slog.Info("cli.compact.apply.started",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"target", target,
		"force", force,
	)
	in := compactengine.ApplyInput{
		Slice:         slice,
		SessionID:     sess.Metadata.SessionID,
		Cwd:           sess.Metadata.WorkspaceRoot,
		Version:       "clyde",
		Strippers:     strippers,
		Target:        target,
		BoundaryTail:  planRes.BoundaryTail,
		PreCompactTok: planRes.BaselineTail,
		Force:         force,
	}
	res, err := compactengine.Apply(in)
	if err != nil {
		slog.Error("cli.compact.apply.failed",
			"session", sess.Name,
			"session_id", sess.Metadata.SessionID,
			slog.Any("err", err),
		)
		return err
	}
	_, _ = fmt.Fprintln(out, "  verified appended transcript lines as valid JSON boundary/synthetic pair")
	_, _ = fmt.Fprintf(out, "\napplied:\n  boundary uuid:   %s\n  synthetic uuid:  %s\n  pre-apply bytes: %d\n  post-apply bytes: %d\n  snapshot:        %s\n  ledger:          %s\n",
		res.BoundaryUUID, res.SyntheticUUID, res.PreApplyOffset, res.PostApplyOffset, res.SnapshotPath, res.LedgerPath)
	slog.Info("cli.compact.apply.completed",
		"session", sess.Name,
		"session_id", sess.Metadata.SessionID,
		"boundary_uuid", res.BoundaryUUID,
		"synthetic_uuid", res.SyntheticUUID,
		"pre_apply_offset", res.PreApplyOffset,
		"post_apply_offset", res.PostApplyOffset,
	)
	return nil
}
