package tuiqa

import (
	"fmt"
	"path/filepath"

	"goodkind.io/clyde/internal/session"
)

// WriteSeedSessions creates one demo session under an isolated XDG data tree.
// xdgDataHome should be the value of XDG_DATA_HOME (clyde uses dataHome/clyde).
func WriteSeedSessions(xdgDataHome string) error {
	clydeRoot := filepath.Join(xdgDataHome, "clyde")
	fs := session.NewFileStore(clydeRoot)
	sess := session.NewSession("tuiqa-demo-01", "00000000-0000-0000-0000-000000000001")
	sess.Metadata.WorkspaceRoot = filepath.Join(xdgDataHome, "demo-workspace")
	if err := fs.Create(sess); err != nil {
		return fmt.Errorf("seed session: %w", err)
	}
	return nil
}
