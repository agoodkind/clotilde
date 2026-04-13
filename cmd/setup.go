package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

const launchAgentLabel = "io.goodkind.clotilde.daemon"

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register clotilde hooks globally for Claude Code",
		Long: `Register SessionStart hooks in ~/.claude/settings.json so clotilde
works automatically in all projects. Run this once after installing clotilde.

Use --local to install hooks in ~/.claude/settings.local.json instead.
Use --launch-agent to also register the daemon as a Login Item so it
pre-starts at login (optional — clotilde also launches it on demand).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			local, _ := cmd.Flags().GetBool("local")

			if err := claude.IsInstalled(); err != nil {
				return err
			}

			clotildeBinary, err := os.Executable()
			if err != nil {
				return fmt.Errorf("failed to determine clotilde binary path: %w", err)
			}

			homeDir, err := util.HomeDir()
			if err != nil {
				return fmt.Errorf("failed to determine home directory: %w", err)
			}

			settingsFile := "settings.json"
			if local {
				settingsFile = "settings.local.json"
			}

			claudeDir := filepath.Join(homeDir, ".claude")
			settingsPath := filepath.Join(claudeDir, settingsFile)

			// Ensure ~/.claude directory exists
			if err := util.EnsureDir(claudeDir); err != nil {
				return fmt.Errorf("failed to create ~/.claude directory: %w", err)
			}

			hooks, err := mergeHooksIntoSettings(settingsPath, clotildeBinary)
			if err != nil {
				return err
			}

			hooksJSON, _ := json.MarshalIndent(hooks, "  ", "  ")
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Added hooks to ~/%s:\n  %s\n", filepath.Join(".claude", settingsFile), string(hooksJSON))
			_, _ = fmt.Fprintln(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Success("Clotilde setup complete!"))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Sessions will be created automatically when you run:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")

			// Always install LaunchAgent on macOS
			if err := installLaunchAgent(cmd, clotildeBinary, homeDir); err != nil {
				// Non-fatal on non-macOS
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "Warning: LaunchAgent setup failed: %v\n", err)
			}

			return nil
		},
	}

	cmd.Flags().Bool("local", false, "Install hooks in ~/.claude/settings.local.json instead of settings.json")

	return cmd
}

// installLaunchAgent writes a LaunchAgent plist for the clotilde daemon and
// bootstraps it with launchctl so it starts at the next login.
func installLaunchAgent(cmd *cobra.Command, clotildeBinary, homeDir string) error {
	logDir := config.DefaultStateDir()
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("failed to create log dir: %w", err)
	}

	agentDir := filepath.Join(homeDir, "Library", "LaunchAgents")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("failed to create LaunchAgents dir: %w", err)
	}

	plistPath := filepath.Join(agentDir, launchAgentLabel+".plist")
	logPath := filepath.Join(logDir, "daemon.log")

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<false/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, launchAgentLabel, clotildeBinary, logPath, logPath)

	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("failed to write LaunchAgent plist: %w", err)
	}

	uid := strconv.Itoa(os.Getuid())

	// Unload any existing registration before re-bootstrapping.
	_ = exec.Command("launchctl", "bootout", "gui/"+uid, plistPath).Run()

	if out, err := exec.Command("launchctl", "bootstrap", "gui/"+uid, plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %w\n%s", err, out)
	}

	_, _ = fmt.Fprintln(cmd.OutOrStdout())
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Success("LaunchAgent registered: "+launchAgentLabel))
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Daemon will pre-start at login.")
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Plist: "+plistPath)

	return nil
}
