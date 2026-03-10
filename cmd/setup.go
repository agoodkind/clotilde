package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/claude"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/ui"
	"github.com/fgrehm/clotilde/internal/util"
)

func newSetupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Register clotilde hooks globally for Claude Code",
		Long: `Register SessionStart hooks in ~/.claude/settings.json so clotilde
works automatically in all projects. Run this once after installing clotilde.

Use --local to install hooks in ~/.claude/settings.local.json instead.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			local, _ := cmd.Flags().GetBool("local")
			zellijTabStatus, _ := cmd.Flags().GetBool("zellij-tab-status")

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

			// Handle --zellij-tab-status opt-in
			if zellijTabStatus {
				globalCfg, err := config.LoadGlobalOrDefault()
				if err != nil {
					return fmt.Errorf("failed to load global config: %w", err)
				}
				globalCfg.ZellijTabStatus = true
				if err := config.SaveGlobal(globalCfg); err != nil {
					return fmt.Errorf("failed to save global config: %w", err)
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout())
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Success("Zellij tab status enabled!"))
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Requires the zellij-tab-name plugin. Add to your Zellij config:")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  load_plugins {")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), `    "https://github.com/Cynary/zellij-tab-name/releases/download/v0.4.2/zellij-tab-name.wasm"`)
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  }")
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Then restart Zellij for the plugin to load.")
			}

			_, _ = fmt.Fprintln(cmd.OutOrStdout())
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), ui.Success("Clotilde setup complete!"))
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  Sessions will be created automatically when you run:")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "  clotilde start <session-name>")

			return nil
		},
	}

	cmd.Flags().Bool("local", false, "Install hooks in ~/.claude/settings.local.json instead of settings.json")
	cmd.Flags().Bool("zellij-tab-status", false, "Enable Zellij tab renaming with emoji status indicators")

	return cmd
}
