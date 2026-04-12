package cmd

// InvocationMode describes how clotilde should handle a given set of CLI args.
type InvocationMode int

const (
	// ModeClotilde: args describe a native clotilde subcommand — hand to cobra as-is.
	ModeClotilde InvocationMode = iota

	// ModePassthrough: args belong to the real claude binary (internal mechanism,
	// non-interactive pipe, or explicit print/api call) — forward transparently.
	ModePassthrough

	// ModeResumeFlag: user called --resume / -r (claude's native flag form).
	// Rewrite to the clotilde resume subcommand before cobra sees it.
	ModeResumeFlag
)

// clotildeSubcommands is the set of subcommand names that clotilde owns.
// Anything not in this set is either a flag to be rewritten or a passthrough.
var clotildeSubcommands = map[string]bool{
	"start":      true,
	"resume":     true,
	"fork":       true,
	"incognito":  true,
	"list":       true,
	"ls":         true,
	"inspect":    true,
	"rename":     true,
	"mv":         true,
	"delete":     true,
	"export":     true,
	"adopt":      true,
	"setup":      true,
	"init":       true,
	"hook":       true,
	"version":    true,
	"completion": true,
	// help and --help / -h are handled by cobra automatically
}

// passthroughSubcommands are claude-internal subcommands that must bypass clotilde.
var passthroughSubcommands = map[string]bool{
	"exec": true, // Claude Code's internal subprocess mechanism
	"api":  true, // Claude API subcommand
}

// ClassifyArgs inspects os.Args (without the binary name) and returns the
// InvocationMode that should apply, plus the rewritten args clotilde should use.
//
// Decision table:
//
//	--resume <uuid> / -r <uuid>          → ModeResumeFlag  (rewrite to: resume <uuid>)
//	<known clotilde subcommand>          → ModeClotilde    (no rewrite)
//	<claude-internal subcommand>         → ModePassthrough (forward verbatim)
//	--print / -p                         → ModePassthrough (non-interactive query)
//	stdin not a TTY + no subcommand      → ModePassthrough (pipe / script mode)
//	anything else (unknown flags/cmds)   → ModeClotilde    (let cobra handle / passthrough)
func ClassifyArgs(args []string) (mode InvocationMode, rewritten []string) {
	if len(args) == 0 {
		return ModeClotilde, args
	}

	first := args[0]

	// ── Resume flag forms ──────────────────────────────────────────────────────
	if first == "--resume" || first == "-r" {
		return ModeResumeFlag, append([]string{"resume"}, args[1:]...)
	}

	// ── Claude-internal subcommands ────────────────────────────────────────────
	if passthroughSubcommands[first] {
		return ModePassthrough, args
	}

	// ── Explicit print / non-interactive query mode ───────────────────────────
	// claude -p "query" or claude --print "query" runs a single non-interactive
	// query and exits. Clotilde has no equivalent; forward to the real binary.
	if first == "--print" || first == "-p" {
		return ModePassthrough, args
	}

	// ── Known clotilde subcommand ─────────────────────────────────────────────
	if clotildeSubcommands[first] {
		return ModeClotilde, args
	}

	// ── Everything else ────────────────────────────────────────────────────────
	// Includes unknown flags (--debug, --model used at top level, etc.) and
	// unknown subcommands. Hand to cobra; Execute()'s unknown-command handler
	// will forward anything cobra can't parse.
	return ModeClotilde, args
}
