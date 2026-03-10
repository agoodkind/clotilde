package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// PipeTimeout is the maximum time to wait for zellij pipe to complete.
var PipeTimeout = 2 * time.Second

// TabRenamer renames the Zellij tab containing the current pane.
type TabRenamer interface {
	RenameTab(name string) error
}

// ZellijPipeRenamer renames tabs via the zellij-tab-name plugin using
// `zellij pipe`. Targets the tab by pane ID so it renames the correct tab
// even when another tab is focused. If the plugin is not installed, the
// pipe message is silently dropped.
//
// Requires: https://github.com/Cynary/zellij-tab-name
type ZellijPipeRenamer struct{}

func (z *ZellijPipeRenamer) RenameTab(name string) error {
	paneID := os.Getenv("ZELLIJ_PANE_ID")
	if paneID == "" {
		return fmt.Errorf("ZELLIJ_PANE_ID not set")
	}

	payload, err := json.Marshal(map[string]string{
		"pane_id": paneID,
		"name":    name,
	})
	if err != nil {
		return fmt.Errorf("marshaling pipe payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), PipeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "zellij", "pipe", "--name", "change-tab-name", "--", string(payload))
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("zellij pipe failed: %w: %s", err, out)
	}
	return nil
}
