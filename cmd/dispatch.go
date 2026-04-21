package cmd

import "log/slog"

// InvocationMode describes how clyde should handle a given set of CLI args.
type InvocationMode int

const (
	// ModeClyde: args describe a native clyde subcommand  --  hand to cobra as-is.
	ModeClyde InvocationMode = iota

	// ModePassthrough: args belong to the real claude binary (internal mechanism,
	// non-interactive pipe, or explicit print/api call)  --  forward transparently.
	ModePassthrough

	// ModeResumeFlag: user called --resume / -r (claude's native flag form).
	// Rewrite to the clyde resume subcommand before cobra sees it.
	ModeResumeFlag
)

// clydeSubcommands is the set of subcommand names that clyde owns.
// Anything not in this set is either a flag to be rewritten or a
// passthrough. Current surface: TUI (no-arg), compact, daemon, hook,
// mcp, resume. help and --help / -h are handled by cobra.
var clydeSubcommands = map[string]bool{
	"compact": true,
	"daemon":  true,
	"hook":    true,
	"mcp":     true,
	"resume":  true,
}

// passthroughSubcommands are claude-internal subcommands that must
// bypass clyde and go straight to the real claude binary.
var passthroughSubcommands = map[string]bool{
	"exec": true, // Claude Code's internal subprocess mechanism
	"api":  true, // Claude API subcommand
}

// ClassifyArgs inspects os.Args (without the binary name) and returns the
// InvocationMode that should apply, plus the rewritten args clyde should use.
//
// Decision table:
//
//	--resume <uuid> / -r <uuid>          → ModeResumeFlag  (rewrite to: resume <uuid>)
//	<known clyde subcommand>               → ModeClyde       (no rewrite)
//	<claude-internal subcommand>         → ModePassthrough (forward verbatim)
//	--print / -p                         → ModePassthrough (non-interactive query)
//	stdin not a TTY + no subcommand      → ModePassthrough (pipe / script mode)
//	anything else (unknown flags/cmds)   → ModeClyde       (let cobra handle / passthrough)
func ClassifyArgs(args []string) (mode InvocationMode, rewritten []string) {
	log := slog.Default().With("component", "cli", "subcomponent", "dispatch")
	var firstArg string
	if len(args) > 0 {
		firstArg = args[0]
	}
	log.Debug("cli.args.classify.invoked",
		"argc", len(args),
		"first_arg", firstArg,
	)

	if len(args) == 0 {
		log.Info(
			"cli.args.classify.decided",
			"argc", len(args),
			"mode", "clyde",
			"decision", "empty_args",
		)
		return ModeClyde, args
	}

	first := firstArg
	isPassthrough := passthroughSubcommands[first]
	isClyde := clydeSubcommands[first]
	isResume := first == "--resume" || first == "-r"
	isPrint := first == "--print" || first == "-p"

	// ── Resume flag forms ──────────────────────────────────────────────────────
	if first == "--resume" || first == "-r" {
		rewritten = append([]string{"resume"}, args[1:]...)
		log.Info("cli.args.classify.decided",
			"argc", len(args),
			"first_arg", first,
			"mode", "resume",
			"decision", "resume_flag",
			"rewritten_argc", len(rewritten),
			"rewritten", true,
			"is_resume", isResume,
		)
		return ModeResumeFlag, rewritten
	}

	// ── Claude-internal subcommands ────────────────────────────────────────────
	if passthroughSubcommands[first] {
		log.Info("cli.args.classify.decided",
			"argc", len(args),
			"first_arg", first,
			"mode", "passthrough",
			"decision", "passthrough_subcommand",
			"rewritten_argc", len(args),
			"passthrough", isPassthrough,
			"is_clyde_subcommand", isClyde,
		)
		return ModePassthrough, args
	}

	// ── Explicit print / non-interactive query mode ───────────────────────────
	// claude -p "query" or claude --print "query" runs a single non-interactive
	// query and exits. Clyde has no equivalent; forward to the real binary.
	if first == "--print" || first == "-p" {
		log.Info("cli.args.classify.decided",
			"argc", len(args),
			"first_arg", first,
			"mode", "passthrough",
			"decision", "print_flag",
			"rewritten_argc", len(args),
			"is_print", isPrint,
			"is_resume", isResume,
		)
		return ModePassthrough, args
	}

	// ── Known clyde subcommand ─────────────────────────────────────────────
	if clydeSubcommands[first] {
		slog.Info("cli.args.classify.decided",
			"component", "cli",
			"subcomponent", "dispatch",
			"argc", len(args),
			"first_arg", first,
			"mode", "clyde",
			"decision", "clyde_subcommand",
			"rewritten_argc", len(args),
			"is_clyde_subcommand", isClyde,
		)
		return ModeClyde, args
	}

	// ── Everything else ────────────────────────────────────────────────────────
	// Includes unknown flags (--debug, --model used at top level, etc.) and
	// unknown subcommands. Hand to cobra; Execute()'s unknown-command handler
	// will forward anything cobra can't parse.
	slog.Info("cli.args.classify.decided",
		"component", "cli",
		"subcomponent", "dispatch",
		"argc", len(args),
		"first_arg", first,
		"mode", "clyde",
		"decision", "default_to_cobra",
		"rewritten_argc", len(args),
		"passthrough", isPassthrough,
		"is_clyde_subcommand", isClyde,
		"is_resume", isResume,
	)
	return ModeClyde, args
}
