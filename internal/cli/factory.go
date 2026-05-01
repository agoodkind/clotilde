package cli

import (
	"context"
	"log/slog"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/session"
)

// BuildInfo carries the ldflag-injected version metadata so the
// version subcommand and the slog initialiser can both surface it
// without reading package-level globals.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// Factory threads dependencies through the cobra command tree.
//
// Each subpackage under internal/cli receives a *Factory in its
// NewCmd(f *Factory) constructor and resolves the dependencies it
// needs lazily through the closures here. Lazy resolution keeps cobra
// startup cheap and lets tests swap in fakes without touching every
// subcommand. The Factory replaces the package-level globals
// claudeBinaryPath, verbose, and globalStore() that lived in the old
// flat cmd package.
type Factory struct {
	IOStreams *IOStreams
	Logger    *slog.Logger
	Build     BuildInfo

	// ClaudeBinary returns the path to the claude executable. Honors
	// the hidden --claude-bin flag set by tests; otherwise resolves
	// to the literal "claude" so the user's PATH wins.
	ClaudeBinary func() string

	// Verbose returns true when the user passed --verbose / -v on
	// the root command. Subcommands consult this to decide whether
	// to print extra diagnostic detail to IOStreams.Out.
	Verbose func() bool

	// Config loads the merged global+project configuration. Returns
	// the zero value with no error when no config file exists so
	// subcommands can rely on the defaults without special casing.
	Config func() (*config.Config, error)

	// Store opens (or creates) the global session store. The store
	// is rooted at $XDG_DATA_HOME/clyde/sessions and is the one
	// authoritative source of session metadata across the CLI, the
	// TUI, and the daemon.
	Store func() (*session.FileStore, error)

	// Daemon connects to the running daemon, starting one if needed.
	// Returns an error when the daemon is unreachable; callers may
	// fall back to direct store access when the daemon is optional.
	Daemon func(ctx context.Context) (*daemon.Client, error)
}

// NewSystemFactory wires the production dependencies. Tests build
// their own Factory with stub closures.
func NewSystemFactory(build BuildInfo) *Factory {
	return &Factory{
		IOStreams: SystemIOStreams(),
		Logger:    slog.Default(),
		Build:     build,
		ClaudeBinary: func() string {
			if claudeBinaryPath != "" {
				return claudeBinaryPath
			}
			return "claude"
		},
		Verbose: func() bool { return verbose },
		Config:  config.LoadGlobalOrDefault,
		Store:   session.NewGlobalFileStore,
		Daemon:  daemon.ConnectOrStart,
	}
}
