package adapter

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"goodkind.io/gklog"
)

// Deps are the host hooks the adapter needs from the daemon process.
// The daemon owns the real implementations (findRealClaude and the
// scratch directory helper); the adapter accepts them as fields so
// the package stays testable without pulling the daemon in.
type Deps struct {
	// ResolveClaude returns the path to the real claude binary.
	ResolveClaude func() (string, error)
	// ScratchDir returns a clyde owned cwd for the subprocess.
	// Empty string is tolerated; the runner falls back to the
	// current working directory.
	ScratchDir func() string
}

// Runner spawns claude -p for one request. Each HTTP request builds
// a Runner, drives it, then drops it. The runner holds no state
// between calls so cancellation, concurrency, and auth stay with
// the HTTP layer above it.
type Runner struct {
	deps   Deps
	model  ResolvedModel
	effort string
	system string
	prompt string
	logTag string
}

// BuildPrompt collapses the OpenAI message array into a single user
// prompt string plus an optional system prompt. Multiple system
// messages join with a blank line. User and assistant messages are
// framed "role: text" so claude sees turn boundaries. This is the
// same trick openclaw-claude-bridge uses for its v1.
func BuildPrompt(messages []ChatMessage) (system, prompt string) {
	var sys []string
	var body []string
	for _, m := range messages {
		text := FlattenContent(m.Content)
		switch strings.ToLower(m.Role) {
		case "system", "developer":
			if text != "" {
				sys = append(sys, text)
			}
		case "user":
			body = append(body, "user: "+text)
		case "assistant":
			body = append(body, "assistant: "+text)
		case "tool":
			body = append(body, "tool: "+text)
		default:
			body = append(body, m.Role+": "+text)
		}
	}
	return strings.Join(sys, "\n\n"), strings.Join(body, "\n\n")
}

// NewRunner constructs a Runner for the given resolved model.
func NewRunner(deps Deps, model ResolvedModel, effort, system, prompt, tag string) *Runner {
	return &Runner{
		deps:   deps,
		model:  model,
		effort: effort,
		system: system,
		prompt: prompt,
		logTag: tag,
	}
}

// Spawn launches claude -p and returns stdout as an io.ReadCloser
// plus a cancel func that kills the subprocess. The caller is
// responsible for consuming stdout and invoking cancel.
func (r *Runner) Spawn(ctx context.Context) (io.ReadCloser, func(), error) {
	log := gklog.LoggerFromContext(ctx).With(
		"component", "adapter",
		"subcomponent", "runner",
		"log_tag", r.logTag,
	)
	log.DebugContext(ctx, "adapter.runner.spawn_requested",
		"model", r.model.ClaudeModel,
		"effort", r.effort,
		"prompt_len", len(r.prompt),
		"has_system_prompt", r.system != "",
	)
	if r.deps.ResolveClaude == nil {
		err := fmt.Errorf("adapter: ResolveClaude not wired")
		log.ErrorContext(ctx, "adapter.runner.resolve_deps_missing", "err", err)
		return nil, nil, err
	}
	bin, err := r.deps.ResolveClaude()
	if err != nil {
		log.ErrorContext(ctx, "adapter.runner.resolve_claude_failed",
			"model", r.model,
			"err", err,
		)
		return nil, nil, fmt.Errorf("resolve claude: %w", err)
	}
	args := r.buildArgs()
	log.DebugContext(ctx, "adapter.runner.command_prepared",
		"arg_count", len(args),
		"effort", r.effort,
	)
	cctx, cancel := context.WithCancel(ctx)
	log = log.With("binary", bin)
	started := time.Now()
	cmd := exec.CommandContext(cctx, bin, args...)
	if r.deps.ScratchDir != nil {
		scratchDir := r.deps.ScratchDir()
		if scratchDir != "" {
			cmd.Dir = scratchDir
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.ErrorContext(cctx, "adapter.runner.stdout_pipe_failed", "err", err)
		cancel()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into stdout for logging
	if err := cmd.Start(); err != nil {
		log.ErrorContext(cctx, "adapter.runner.start_failed", "err", err)
		cancel()
		return nil, nil, fmt.Errorf("start claude: %w", err)
	}
	log.InfoContext(cctx, "adapter.runner.started", "pid", cmd.Process.Pid)
	wait := func() {
		waitErr := cmd.Wait()
		durationMs := time.Since(started).Milliseconds()
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		ctxErr := cctx.Err()
		completionCause := "normal_exit"
		if waitErr != nil && ctxErr != nil {
			completionCause = "wait_and_context"
		} else if ctxErr != nil {
			completionCause = "context_done"
		} else if waitErr != nil {
			completionCause = "wait_error"
		}
		if waitErr != nil {
			log.WarnContext(cctx, "adapter.runner.wait_failed",
				"duration_ms", durationMs,
				"exit_code", exitCode,
				"err", waitErr,
				"completion_cause", completionCause,
			)
		} else {
			log.InfoContext(cctx, "adapter.runner.completed",
				"duration_ms", durationMs,
				"exit_code", exitCode,
				"completion_cause", completionCause,
			)
		}
		cancel()
	}
	go wait()
	return stdout, cancel, nil
}

func (r *Runner) buildArgs() []string {
	args := []string{
		"-p",
		"--model", r.model.ClaudeModel,
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	if eff := ClaudeEffortFlag(r.effort); eff != "" {
		args = append(args, "--effort", eff)
	}
	if r.system != "" {
		args = append(args, "--append-system-prompt", r.system)
	}
	args = append(args, r.prompt)
	return args
}
