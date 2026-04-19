// Command clyde is the user facing entrypoint.
//
// The cobra root is assembled here because this is the only place in
// the build graph that can import both goodkind.io/clyde/internal/cli
// (for Factory + IOStreams) and the per-verb sub-packages under
// internal/cli/<verb>. Putting the assembly inside internal/cli would
// create an import cycle.
//
// Argument surface:
//
//	clyde                       -> TUI dashboard (cmd.RunDashboard)
//	clyde compact ...           -> append-only compaction
//	clyde daemon                -> long-lived daemon (adapter, oauth, mcp, prune)
//	clyde hook sessionstart     -> Claude Code SessionStart hook
//	clyde mcp                   -> MCP stdio server (in-chat search/list/context)
//	clyde resume <name|uuid>    -> resolve clyde name then claude --resume <uuid>
//	clyde -r / --resume <x>     -> rewritten to `clyde resume <x>` by ClassifyArgs
//	anything else               -> ForwardToClaude (transparent passthrough)
package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/clyde/cmd"
	"goodkind.io/clyde/internal/cli"
	"goodkind.io/clyde/internal/cli/compact"
	"goodkind.io/clyde/internal/cli/daemon"
	hook "goodkind.io/clyde/internal/cli/hook"
	"goodkind.io/clyde/internal/cli/mcp"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/slogger"
)

func main() {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "config load failed:", err)
		os.Exit(1)
	}

	closer, err := slogger.Setup(cfg.Logging.Level)
	if err != nil {
		slog.Error("clyde.slogger.setup_failed",
			"component", "cli",
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintln(os.Stderr, "slogger setup failed:", err)
		os.Exit(1)
	}
	defer func() { _ = closer.Close() }()

	if len(os.Args) > 1 {
		mode, rewritten := cmd.ClassifyArgs(os.Args[1:])
		switch mode {
		case cmd.ModePassthrough:
			os.Exit(cmd.ForwardToClaude(os.Args[1:]))
		case cmd.ModeResumeFlag:
			os.Args = append(os.Args[:1], rewritten...)
		}
	}

	slog.Debug("cli.execute.invoked", "component", "cli")

	root := &cobra.Command{
		Use:     "clyde",
		Short:   "Named sessions and append-only compaction for Claude Code",
		Long:    `Clyde wraps Claude Code with human-friendly session names and append-only compaction. Run with no args for the TUI dashboard.`,
		Version: "DEVELOPMENT",
		Run:     cmd.RunDashboard,
	}
	root.CompletionOptions.DisableDefaultCmd = true

	cli.RegisterGlobalFlags(root)

	f := cli.NewSystemFactory(cli.BuildInfo{Version: "DEVELOPMENT"})

	root.SetIn(f.IOStreams.In)
	root.SetOut(f.IOStreams.Out)
	root.SetErr(f.IOStreams.Err)

	root.AddCommand(compact.NewCmd(f))
	root.AddCommand(daemon.NewCmd(f))
	root.AddCommand(hook.NewCmd(f))
	root.AddCommand(mcp.NewCmd(f))
	root.AddCommand(cmd.NewResumeCmd())

	root.SilenceErrors = true

	if err := root.Execute(); err != nil {
		if strings.HasPrefix(err.Error(), "unknown command") {
			os.Exit(cmd.ForwardToClaude(os.Args[1:]))
		}
		_, _ = fmt.Fprintln(f.IOStreams.Err, "Error:", err)
		os.Exit(1)
	}
	slog.Info("cli.execute.completed", "component", "cli")
}
