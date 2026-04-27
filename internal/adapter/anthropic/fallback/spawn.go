// Package fallback contains Anthropic CLI fallback runtime helpers.
package fallback

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	adapterconfig "goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/mitm"
)

// stderrTailLimit caps how much stderr we hold per spawn before discarding
// the head. claude -p's failure messages are short; 16 KiB is generous and
// bounded enough to log safely.
const stderrTailLimit = 16 * 1024

// SpawnInfo describes one spawned subprocess. The wait closure populates
// ExitCode, Duration, and StderrTail; callers should slog those after
// stdout is drained.
type SpawnInfo struct {
	Binary     string
	Args       []string
	ScratchDir string
	RequestID  string
	Model      string

	// Set by wait().
	ExitCode    int
	Duration    time.Duration
	StderrTail  string
	StderrBytes int
}

// spawn launches the subprocess and returns its stdout reader plus
// a wait closure that reports the exit error and populates info.
// stderr is captured separately into a bounded buffer so non-JSON
// failure messages from claude -p (auth errors, panics) are not
// silently swallowed by the stream-json parser.
func (c *Client) spawn(ctx context.Context, r Request) (io.ReadCloser, func() error, *SpawnInfo, error) {
	if r.Model == "" {
		return nil, nil, nil, errors.New("fallback.Request.Model is empty (no CLI alias bound to family)")
	}
	args := buildArgs(r)

	cmd := exec.CommandContext(ctx, c.cfg.Binary, args...)
	if r.WorkspaceDir != "" {
		cmd.Dir = r.WorkspaceDir
	} else {
		cmd.Dir = c.cfg.ScratchDir
	}
	cmd.Env = c.buildEnv()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderrBuf := &boundedBuffer{limit: stderrTailLimit}
	cmd.Stderr = stderrBuf

	info := &SpawnInfo{
		Binary:     c.cfg.Binary,
		Args:       args,
		ScratchDir: c.cfg.ScratchDir,
		RequestID:  r.RequestID,
		Model:      r.Model,
	}

	started := time.Now()
	if err := cmd.Start(); err != nil {
		slog.Error("fallback.spawn.start_failed",
			"subcomponent", "fallback",
			"binary", c.cfg.Binary,
			"request_id", r.RequestID,
			"model", r.Model,
			"err", err.Error(),
		)
		return nil, nil, nil, fmt.Errorf("start claude -p: %w", err)
	}
	slog.Debug("fallback.spawn.start",
		"subcomponent", "fallback",
		"request_id", r.RequestID,
		"binary", c.cfg.Binary,
		"model", r.Model,
		"scratch_dir", c.cfg.ScratchDir,
		"args_len", len(args),
	)

	wait := func() error {
		err := cmd.Wait()
		info.Duration = time.Since(started)
		info.StderrTail = stderrBuf.String()
		info.StderrBytes = stderrBuf.totalWritten
		info.ExitCode = exitCodeOf(err, cmd.ProcessState)
		level := slog.LevelDebug
		if err != nil {
			level = slog.LevelWarn
		}
		slog.LogAttrs(context.Background(), level, "fallback.spawn.exited",
			slog.String("subcomponent", "fallback"),
			slog.String("request_id", r.RequestID),
			slog.String("model", r.Model),
			slog.Int("exit_code", info.ExitCode),
			slog.Int64("duration_ms", info.Duration.Milliseconds()),
			slog.Int("stderr_bytes", info.StderrBytes),
			slog.String("stderr_tail", info.StderrTail),
		)
		return err
	}
	return stdout, wait, info, nil
}

// exitCodeOf extracts the numeric exit code from a Wait error. Returns
// 0 on clean exit, the OS-reported code on ExitError, and -1 when the
// process was killed by signal or never started.
func exitCodeOf(err error, ps *os.ProcessState) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	if ps != nil {
		return ps.ExitCode()
	}
	return -1
}

// boundedBuffer is an io.Writer that retains at most limit bytes (the
// tail), tracking total bytes written for the structured log event.
type boundedBuffer struct {
	limit        int
	buf          bytes.Buffer
	totalWritten int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.totalWritten += len(p)
	if b.limit <= 0 {
		return len(p), nil
	}
	if len(p) >= b.limit {
		b.buf.Reset()
		b.buf.Write(p[len(p)-b.limit:])
		return len(p), nil
	}
	if b.buf.Len()+len(p) > b.limit {
		drop := b.buf.Len() + len(p) - b.limit
		tail := b.buf.Bytes()[drop:]
		b.buf.Reset()
		b.buf.Write(tail)
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	return b.buf.String()
}

// buildArgs renders the full argv for a `claude -p` invocation. Kept
// separate from spawn so tests can assert flag presence without
// executing the subprocess.
func buildArgs(r Request) []string {
	args := []string{
		"-p",
		"--model", r.Model,
		"--output-format", "stream-json",
		"--verbose",
		"--dangerously-skip-permissions",
	}
	switch {
	case r.Resume && r.SessionID != "":
		// Resume reads the synthesized transcript at
		// ~/.claude/projects/<sanitize(cwd)>/<session-id>.jsonl and
		// loads the history verbatim. Incompatible with --session-id
		// (which would create a fresh session), so drop it.
		args = append(args, "--resume", r.SessionID)
	case r.SessionID != "":
		// Stable per-conversation UUID lets Claude Code reuse the same
		// transcript file across back-to-back invocations, which
		// stabilizes the byte sequence the upstream prompt cache hashes
		// against. Cache hits are visible via cache_read_tokens in the
		// adapter.chat.completed log event.
		args = append(args, "--session-id", r.SessionID)
	}
	sys := mergeSystemPrompt(r.System, renderToolsPreamble(r.Tools, r.ToolChoice))
	if sys != "" {
		args = append(args, "--append-system-prompt", sys)
	}
	args = append(args, renderPromptForRequest(r))
	return args
}

// renderPromptForRequest returns the positional prompt for the CLI
// invocation. In the direct prompt path (r.Resume==false), we flatten
// the whole history into the prompt. In the resume path the history
// lives in the transcript file on disk, so we pass only the latest
// user message and let Claude continue from there.
func renderPromptForRequest(r Request) string {
	if !r.Resume {
		return renderPrompt(r.Messages)
	}
	for i := len(r.Messages) - 1; i >= 0; i-- {
		m := r.Messages[i]
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
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
	if cfg, err := adapterconfig.LoadGlobalOrDefault(); err == nil {
		if extra, mitmErr := mitm.ClaudeEnv(context.Background(), cfg.MITM, slog.Default()); mitmErr == nil {
			for key, value := range extra {
				env = append(env, fmt.Sprintf("%s=%s", key, value))
			}
		}
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
