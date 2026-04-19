// Config validation, Client constructor, and Collect/Stream entry points.
package fallback

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Config is the per-Client wiring the parent passes in once at
// startup. Binary is required (the parent resolves the daemon's
// findRealClaude beforehand and stores the result). Timeout caps
// every subprocess. ScratchDir is the cwd for every spawn.
type Config struct {
	// Binary is the absolute path to the `claude` CLI.
	Binary string
	// Timeout is the per-request wall clock.
	Timeout time.Duration
	// ScratchDir is the cwd for each spawned subprocess. Created
	// lazily by the parent before passing in.
	ScratchDir string
	// SuppressHookEnv, when true, sets CLYDE_DISABLE_DAEMON=1 and
	// CLYDE_SUPPRESS_HOOKS=1 on the spawned subprocess so a
	// SessionStart hook chain does not recurse back into the
	// daemon.
	SuppressHookEnv bool
}

// Validate returns an error for any required field that is empty or
// non-positive. The parent calls this from buildFallbackConfig so
// the daemon refuses to start the listener with a partial config.
func (c Config) Validate() error {
	if c.Binary == "" {
		return errors.New("fallback.Config.Binary is empty")
	}
	if c.Timeout <= 0 {
		return errors.New("fallback.Config.Timeout must be > 0")
	}
	if c.ScratchDir == "" {
		return errors.New("fallback.Config.ScratchDir is empty")
	}
	return nil
}

// Client wraps a validated Config. Methods are safe to call from
// multiple goroutines; the subprocess wiring is per-call.
type Client struct {
	cfg Config
}

// New returns a Client. Validate must have already been called by
// the parent during buildFallbackConfig; New does not re-validate.
func New(cfg Config) *Client {
	return &Client{cfg: cfg}
}

// Collect runs `claude -p` and returns the joined assistant text
// plus usage counters. Streaming output is parsed internally; the
// caller does not see deltas.
func (c *Client) Collect(ctx context.Context, r Request) (Result, error) {
	cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	stdout, wait, err := c.spawn(cctx, r)
	if err != nil {
		return Result{}, err
	}
	text, reasoning, usage, stopReason, parseErr := collectStreamJSON(stdout)
	waitErr := wait()
	if parseErr != nil {
		return Result{}, parseErr
	}
	if waitErr != nil {
		return Result{}, fmt.Errorf("claude -p exited: %w", waitErr)
	}
	return finalizeAssistantText(text, reasoning, r, usage, stopReason), nil
}

// Stream runs `claude -p` and invokes onEvent with each text or reasoning
// fragment in arrival order when toolEnvelopeActive(r) is false.
//
// When toolEnvelopeActive(r) is true, stdout is buffered instead of
// streaming deltas because partial JSON envelopes interleaved with
// prose break OpenAI clients that expect discrete tool_calls deltas.
// The caller should emit synthetic stream chunks after Stream returns.
func (c *Client) Stream(ctx context.Context, r Request, onEvent func(StreamEvent) error) (StreamResult, error) {
	cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	stdout, wait, err := c.spawn(cctx, r)
	if err != nil {
		return StreamResult{}, err
	}
	fullText, fullReasoning, usage, stopReason, parseErr := streamStreamJSON(stdout, r, onEvent)
	waitErr := wait()
	if parseErr != nil {
		return StreamResult{Usage: usage}, parseErr
	}
	if waitErr != nil {
		return StreamResult{Usage: usage}, fmt.Errorf("claude -p exited: %w", waitErr)
	}
	res := finalizeAssistantText(fullText, fullReasoning, r, usage, stopReason)
	return StreamResult{
		Usage:            res.Usage,
		Stop:             res.Stop,
		Text:             res.Text,
		ReasoningContent: res.ReasoningContent,
		Refusal:          res.Refusal,
		ToolCalls:        res.ToolCalls,
	}, nil
}

// EnsureScratchDir creates the cwd path beneath base for the
// fallback subprocess. The parent calls this once during
// buildFallbackConfig; failure aborts daemon startup.
func EnsureScratchDir(base, subdir string) (string, error) {
	if base == "" {
		return "", errors.New("fallback.EnsureScratchDir: base is empty")
	}
	if subdir == "" {
		return "", errors.New("fallback.EnsureScratchDir: subdir is empty")
	}
	dir := filepath.Join(base, subdir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("fallback.EnsureScratchDir mkdir %s: %w", dir, err)
	}
	return dir, nil
}
