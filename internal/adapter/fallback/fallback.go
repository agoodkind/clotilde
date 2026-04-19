// Package fallback drives the local `claude` CLI in
// `-p --output-format stream-json` mode as the third adapter
// backend. It exists so the OpenAI adapter can either explicitly
// route an alias through `claude -p` or escalate to it when the
// direct-OAuth path returns an error. The package is deliberately
// isolated from the parent `adapter` package so the registry and
// HTTP layers stay slim and so the subprocess details (env
// suppression, scratch dir, stream-json parser) live in one place.
//
// There are no compiled-in defaults: the parent registry validates
// every required field on construction. This package assumes a
// fully populated Config and panics nowhere; bad input surfaces as
// an error from Collect or Stream.
package fallback

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

// Message is one turn in the conversation. The driver collapses the
// alternating sequence into a single prompt blob for `claude -p`
// because the CLI takes one positional prompt argument.
type Message struct {
	Role    string // "user" | "assistant" | "system" | other
	Content string
}

// Request describes one fallback dispatch. Model is the CLI short
// name (e.g. "opus", "sonnet", "haiku") taken from
// ResolvedModel.CLIAlias by the dispatcher.
type Request struct {
	Model      string
	System     string
	Messages   []Message
	Tools      []Tool
	ToolChoice string
	RequestID  string
}

// Usage is the token accounting echoed back from the result frame.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Result is the non-streaming return shape: the joined assistant
// text plus the usage counters from the result frame.
type Result struct {
	Text      string
	ToolCalls []ToolCall
	Usage     Usage
	Stop      string // "tool_calls" when ToolCalls is non-empty, else "stop"
}

// StreamResult is the streaming completion outcome after stdout closes.
type StreamResult struct {
	Usage     Usage
	Stop      string
	Text      string
	ToolCalls []ToolCall
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
	text, usage, parseErr := collectStreamJSON(stdout)
	waitErr := wait()
	if parseErr != nil {
		return Result{}, parseErr
	}
	if waitErr != nil {
		return Result{}, fmt.Errorf("claude -p exited: %w", waitErr)
	}
	return finalizeAssistantText(text, r, usage), nil
}

// Stream runs `claude -p` and invokes onDelta with each text chunk
// in arrival order when toolEnvelopeActive(r) is false.
//
// When toolEnvelopeActive(r) is true, stdout text is buffered instead
// of streaming deltas because partial JSON envelopes interleaved with
// prose break OpenAI clients that expect discrete tool_calls deltas.
// The caller should emit synthetic stream chunks after Stream returns.
func (c *Client) Stream(ctx context.Context, r Request, onDelta func(string) error) (StreamResult, error) {
	cctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	stdout, wait, err := c.spawn(cctx, r)
	if err != nil {
		return StreamResult{}, err
	}
	fullText, usage, parseErr := streamStreamJSON(stdout, r, onDelta)
	waitErr := wait()
	if parseErr != nil {
		return StreamResult{Usage: usage}, parseErr
	}
	if waitErr != nil {
		return StreamResult{Usage: usage}, fmt.Errorf("claude -p exited: %w", waitErr)
	}
	res := finalizeAssistantText(fullText, r, usage)
	return StreamResult{
		Usage:     res.Usage,
		Stop:      res.Stop,
		Text:      res.Text,
		ToolCalls: res.ToolCalls,
	}, nil
}

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

// claudeEvent mirrors the subset of `claude -p --output-format
// stream-json` events the parser needs. Tolerant of unknown fields
// so future CLI additions do not break parsing.
type claudeEvent struct {
	Type    string         `json:"type"`
	Subtype string         `json:"subtype,omitempty"`
	Message claudeMessage  `json:"message,omitempty"`
	Usage   claudeRawUsage `json:"usage,omitempty"`
}

type claudeMessage struct {
	Content []claudeContent `json:"content"`
}

type claudeContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeRawUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// collectStreamJSON drains stream-json from r, joins assistant text,
// and reports the usage counts from the terminal `result` frame.
func collectStreamJSON(r io.Reader) (string, Usage, error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var sb strings.Builder
	var usage Usage
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type == "text" {
					sb.WriteString(c.Text)
				}
			}
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return sb.String(), usage, fmt.Errorf("fallback collect scan: %w", err)
	}
	return sb.String(), usage, nil
}

// streamStreamJSON drains stream-json from r and invokes onDelta
// with each assistant text chunk unless toolEnvelopeActive(req) is
// true, in which case text is joined into fullText for post-parse
// envelope handling.
func streamStreamJSON(r io.Reader, req Request, onDelta func(string) error) (fullText string, usage Usage, err error) {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 1<<20)
	sc.Buffer(buf, 8<<20)
	var sb strings.Builder
	bufferTools := toolEnvelopeActive(req)
	for sc.Scan() {
		ev, ok := decodeEvent(sc.Bytes())
		if !ok {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				if c.Type != "text" || c.Text == "" {
					continue
				}
				if bufferTools {
					sb.WriteString(c.Text)
					continue
				}
				if err := onDelta(c.Text); err != nil {
					return "", usage, err
				}
			}
		case "result":
			usage = Usage{
				PromptTokens:     ev.Usage.InputTokens,
				CompletionTokens: ev.Usage.OutputTokens,
				TotalTokens:      ev.Usage.InputTokens + ev.Usage.OutputTokens,
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", usage, fmt.Errorf("fallback stream scan: %w", err)
	}
	return sb.String(), usage, nil
}

func decodeEvent(line []byte) (claudeEvent, bool) {
	trim := strings.TrimSpace(string(line))
	if trim == "" {
		return claudeEvent{}, false
	}
	var ev claudeEvent
	if err := json.Unmarshal([]byte(trim), &ev); err != nil {
		return claudeEvent{}, false
	}
	return ev, true
}
