// Subprocess: argv assembly, environment, and working directory.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// spawn launches the subprocess and returns its stdout reader plus
// a wait closure that reports the exit error. The caller is
// responsible for draining stdout before invoking wait.
func (c *Client) spawn(ctx context.Context, r Request) (io.ReadCloser, func() error, error) {
	if r.Model == "" {
		return nil, nil, errors.New("fallback.Request.Model is empty (no CLI alias bound to family)")
	}
	args := []string{
		"-p",
		"--model", r.Model,
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	sys := mergeSystemPrompt(r.System, renderToolsPreamble(r.Tools, r.ToolChoice))
	if sys != "" {
		args = append(args, "--append-system-prompt", sys)
	}
	args = append(args, renderPrompt(r.Messages))

	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	cmd.Dir = c.cfg.ScratchDir
	cmd.Env = c.buildEnv()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start claude -p: %w", err)
	}
	wait := func() error {
		return cmd.Wait()
	}
	return stdout, wait, nil
}

// buildEnv returns the env slice for the subprocess, optionally
// inserting the two suppression vars that break the SessionStart
// hook recursion the daemon used to suffer from.
func (c *Client) buildEnv() []string {
	env := os.Environ()
	if c.cfg.SuppressHookEnv {
		env = append(env,
			"CLYDE_DISABLE_DAEMON=1",
			"CLYDE_SUPPRESS_HOOKS=1",
		)
	}
	return env
}

// renderPrompt collapses the message sequence into one prompt blob
// `claude -p` can take as its positional argument. System content
// is intentionally not duplicated here; it goes via
// --append-system-prompt.
func renderPrompt(msgs []Message) string {
	var parts []string
	for _, m := range msgs {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "system" || role == "developer" {
			continue
		}
		text := strings.TrimSpace(m.Content)
		if text == "" {
			continue
		}
		parts = append(parts, role+": "+text)
	}
	return strings.Join(parts, "\n\n")
}
