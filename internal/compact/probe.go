package compact

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ContextUsage is the payload returned by Claude Code's
// get_context_usage control request. Only the fields compact needs are
// modeled; other fields (gridRows, autocompactSource) are ignored.
type ContextUsage struct {
	Model       string            `json:"model"`
	TotalTokens int               `json:"totalTokens"`
	MaxTokens   int               `json:"maxTokens"`
	Percentage  int               `json:"percentage"`
	Categories  []ContextCategory `json:"categories"`
}

// ContextCategory mirrors the per-row structure Claude Code's /context
// uses internally. Name is free-form; the known stable values are
// "System prompt", "System tools", "MCP tools", "Memory files",
// "Skills", "Messages", "Compact buffer", "Free space", and their
// "(deferred)" variants.
type ContextCategory struct {
	Name       string `json:"name"`
	Tokens     int    `json:"tokens"`
	Color      string `json:"color"`
	IsDeferred bool   `json:"isDeferred"`
}

// categoryNamesExcludedFromOverhead lists the category names whose
// tokens are NOT part of static overhead. Messages is the transcript
// tail that compact trims. Compact buffer matches the --reserved knob
// and is added back by the planner; including it here would double
// count. Free space is a visualization artifact, not real tokens.
var categoryNamesExcludedFromOverhead = map[string]bool{
	"Messages":       true,
	"Compact buffer": true,
	"Free space":     true,
}

// StaticOverheadFromUsage derives the non-trimmable floor from the
// live /context total itself. Claude's category rows are not reliably
// additive, especially once deferred buckets are present, so summing
// every non-message category can materially overstate the floor. When
// totalTokens is present we treat it as the authority and subtract the
// dynamic buckets (Messages, Compact buffer, Free space). If total is
// unavailable, fall back to the older "sum included categories"
// behavior.
func StaticOverheadFromUsage(u ContextUsage) int {
	excluded := 0
	included := 0
	for _, cat := range u.Categories {
		if categoryNamesExcludedFromOverhead[cat.Name] {
			excluded += cat.Tokens
			continue
		}
		included += cat.Tokens
	}
	if u.TotalTokens > 0 {
		floor := u.TotalTokens - excluded
		if floor < 0 {
			return 0
		}
		return floor
	}
	return included
}

// ProbeOptions configures a get_context_usage probe.
type ProbeOptions struct {
	// SessionID resumes the live session so the probe measures the
	// actual per-session overhead (model choice, agent, custom system
	// prompt, injected context). Required.
	SessionID string

	// WorkDir is the cwd used when spawning claude. Must match the
	// session's original workspace so memory files (CLAUDE.md) and
	// skills resolve to the same set the live session sees.
	WorkDir string

	// Binary is the path to the claude CLI. Defaults to "claude" on
	// $PATH when empty. Tests inject a fake binary path here.
	Binary string

	// Timeout caps the probe. claude cold-starts plus MCP servers can
	// take 15-30 seconds on a large workspace, so callers should
	// budget at least 60 seconds for the default.
	Timeout time.Duration

	// ForkSession, when true, resumes the target session through a
	// disposable fork so the probe does not append /context noise to
	// the real transcript that compaction is about to mutate.
	ForkSession bool
}

// ProbeContextUsage spawns claude in SDK stream-json mode against a
// specific session, sends a get_context_usage control request over
// stdin, reads stdout until the matching control_response arrives, and
// returns the parsed ContextUsage. The spawned claude process is
// closed when the function returns. No session persistence side
// effects are written because the probe passes --no-session-persistence.
func ProbeContextUsage(ctx context.Context, opts ProbeOptions) (ContextUsage, error) {
	if opts.SessionID == "" {
		return ContextUsage{}, errors.New("probe: session id required")
	}
	binary := opts.Binary
	if binary == "" {
		binary = "claude"
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	const requestID = "clyde-auto-calibrate-r1"
	controlReq := map[string]any{
		"type":       "control_request",
		"request_id": requestID,
		"request": map[string]any{
			"subtype": "get_context_usage",
		},
	}
	reqLine, err := json.Marshal(controlReq)
	if err != nil {
		return ContextUsage{}, fmt.Errorf("probe: marshal control request: %w", err)
	}
	reqLine = append(reqLine, '\n')

	args := buildProbeArgs(opts)
	cmd := exec.CommandContext(probeCtx, binary, args...)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}
	cmd.Env = append(os.Environ(), "CLYDE_SUPPRESS_HOOKS=1")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return ContextUsage{}, fmt.Errorf("probe: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return ContextUsage{}, fmt.Errorf("probe: stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return ContextUsage{}, fmt.Errorf("probe: stderr pipe: %w", err)
	}

	started := time.Now()
	compactLog.Logger().Info("compact.probe.spawned",
		"component", "compact",
		"subcomponent", "probe",
		"binary", binary,
		"session_id", opts.SessionID,
		"work_dir", opts.WorkDir,
		"timeout_s", int(timeout.Seconds()),
	)

	if err := cmd.Start(); err != nil {
		return ContextUsage{}, fmt.Errorf("probe: start claude: %w", err)
	}

	// Drain stderr concurrently so a noisy claude does not block on a
	// full pipe buffer. Keep the tail for diagnostics on failure.
	var stderrTail strings.Builder
	var stderrWG sync.WaitGroup
	stderrWG.Go(func() {
		buf := make([]byte, 4096)
		for {
			n, readErr := stderr.Read(buf)
			if n > 0 {
				if stderrTail.Len() < 2048 {
					remaining := 2048 - stderrTail.Len()
					if n > remaining {
						stderrTail.Write(buf[:remaining])
					} else {
						stderrTail.Write(buf[:n])
					}
				}
			}
			if readErr != nil {
				return
			}
		}
	})

	if _, err := stdin.Write(reqLine); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		stderrWG.Wait()
		return ContextUsage{}, fmt.Errorf("probe: write control request: %w", err)
	}
	// Close stdin so claude sees EOF and exits after the response.
	// Keeping stdin open would leave the process waiting for more
	// messages until the context deadline.
	if err := stdin.Close(); err != nil {
		compactLog.Logger().Debug("compact.probe.stdin_close_warn",
			"component", "compact",
			"subcomponent", "probe",
			"err", err,
		)
	}

	usage, parseErr := scanForUsage(stdout, requestID)
	waitErr := cmd.Wait()
	stderrWG.Wait()

	durationMs := time.Since(started).Milliseconds()
	if parseErr != nil {
		compactLog.Logger().Warn("compact.probe.parse_failed",
			"component", "compact",
			"subcomponent", "probe",
			"session_id", opts.SessionID,
			"duration_ms", durationMs,
			"stderr_tail", stderrTail.String(),
			slog.Any("err", parseErr),
			slog.Any("wait_err", waitErr),
		)
		return ContextUsage{}, parseErr
	}
	compactLog.Logger().Info("compact.probe.completed",
		"component", "compact",
		"subcomponent", "probe",
		"session_id", opts.SessionID,
		"duration_ms", durationMs,
		"total_tokens", usage.TotalTokens,
		"max_tokens", usage.MaxTokens,
		"percentage", usage.Percentage,
		"model", usage.Model,
		"categories", len(usage.Categories),
	)
	return usage, nil
}

func buildProbeArgs(opts ProbeOptions) []string {
	args := []string{
		"-p",
		"--resume", opts.SessionID,
	}
	if opts.ForkSession {
		probeID := uuid.NewString()
		args = append(args,
			"--fork-session",
			"--session-id", probeID,
			"-n", "clyde-probe-"+probeID[:8],
		)
	}
	args = append(args,
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--no-session-persistence",
	)
	return args
}

// scanForUsage reads stdout line by line looking for a control_response
// whose request_id matches the one we sent. The stream may also carry
// hook events, session events, and other SDK messages that we ignore.
// Returns an error if stdout closes before the matching response is
// seen or if the response is an error rather than success.
func scanForUsage(stdout io.Reader, requestID string) (ContextUsage, error) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 128*1024), 8*1024*1024)

	type controlInner struct {
		Subtype   string          `json:"subtype"`
		RequestID string          `json:"request_id"`
		Error     string          `json:"error,omitempty"`
		Response  json.RawMessage `json:"response,omitempty"`
	}
	type controlEnvelope struct {
		Type     string       `json:"type"`
		Response controlInner `json:"response"`
	}

	lines := 0
	for scanner.Scan() {
		lines++
		line := scanner.Bytes()
		if !hasControlResponsePrefix(line) {
			continue
		}
		var env controlEnvelope
		if err := json.Unmarshal(line, &env); err != nil {
			continue
		}
		if env.Type != "control_response" {
			continue
		}
		if env.Response.RequestID != requestID {
			continue
		}
		if env.Response.Subtype != "success" {
			return ContextUsage{}, fmt.Errorf("probe: claude returned error: %s", env.Response.Error)
		}
		var usage ContextUsage
		if err := json.Unmarshal(env.Response.Response, &usage); err != nil {
			return ContextUsage{}, fmt.Errorf("probe: decode usage payload: %w", err)
		}
		compactLog.Logger().Debug("compact.probe.response_received",
			"component", "compact",
			"subcomponent", "probe",
			"lines_scanned", lines,
			"request_id", requestID,
		)
		return usage, nil
	}
	if err := scanner.Err(); err != nil {
		return ContextUsage{}, fmt.Errorf("probe: scan stdout: %w (lines=%d)", err, lines)
	}
	return ContextUsage{}, fmt.Errorf("probe: no control_response before stdout closed (lines=%d)", lines)
}

// hasControlResponsePrefix avoids the JSON parse cost on the vast
// majority of stream lines that are not control_response messages.
// Stream-json events always start with `{"type":` so a cheap substring
// check rules out user messages, hook events, and partial-message
// chunks before the heavier Unmarshal.
func hasControlResponsePrefix(line []byte) bool {
	return len(line) > 0 && line[0] == '{' && bytes.Contains(line, []byte(`"control_response"`))
}
