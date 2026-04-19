package adapter

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
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
	if r.deps.ResolveClaude == nil {
		return nil, nil, fmt.Errorf("adapter: ResolveClaude not wired")
	}
	bin, err := r.deps.ResolveClaude()
	if err != nil {
		return nil, nil, fmt.Errorf("resolve claude: %w", err)
	}
	args := r.buildArgs()
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, bin, args...)
	if r.deps.ScratchDir != nil {
		if d := r.deps.ScratchDir(); d != "" {
			cmd.Dir = d
		}
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout // fold stderr into stdout for logging
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("start claude: %w", err)
	}
	wait := func() {
		_ = cmd.Wait()
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
