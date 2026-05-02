package compact

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// SummarizeOptions configures a claude -p summarization call.
type SummarizeOptions struct {
	// Binary is the path to the claude CLI. Defaults to "claude" on
	// $PATH when empty.
	Binary string

	// Model passed to claude via --model. Empty leaves claude's default.
	Model string

	// MaxInputBytes caps how much dropped-portion text is sent to the
	// summarizer. Trims from the oldest end (so most recent dropped
	// content survives). Default 200_000 bytes (~50k tokens).
	MaxInputBytes int

	// Timeout caps the subprocess. Default 120s.
	Timeout time.Duration
}

// SummarizeDropped renders the portion of the slice that the current
// SynthOptions will drop, hands it to `claude -p` for summarization,
// and returns the summary text. Uses OAuth (no API key required).
// The summary is intended to be placed into SynthOptions.Summary
// before the final Synthesize call.
//
// Returns an empty string (not an error) when nothing is being
// dropped; callers can always invoke this unconditionally.
func SummarizeDropped(ctx context.Context, slice *Slice, opts SynthOptions, sopts SummarizeOptions) (string, error) {
	droppedText := renderDroppedForSummary(slice, opts)
	if strings.TrimSpace(droppedText) == "" {
		return "", nil
	}

	binary := sopts.Binary
	if binary == "" {
		binary = "claude"
	}
	maxBytes := sopts.MaxInputBytes
	if maxBytes <= 0 {
		maxBytes = 200_000
	}
	timeout := sopts.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}

	if len(droppedText) > maxBytes {
		// Trim from the head so the most recent dropped content survives.
		excess := len(droppedText) - maxBytes
		droppedText = "[...earlier dropped content elided for length...]\n\n" + droppedText[excess:]
	}

	prompt := "The following is a transcript excerpt that is being removed from a long running agent conversation during context compaction. Write a concise recap (under 400 words) that preserves everything a continuing agent needs to know: user goals, decisions made, files touched, tools invoked, outcomes, and any in flight commitments. Use bullet points grouped by topic. Do not summarize the mechanics of the tools themselves; summarize what was accomplished. Do not greet the user. Output the recap only.\n\n" + droppedText

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"-p", "--no-session-persistence"}
	if sopts.Model != "" {
		args = append(args, "--model", sopts.Model)
	}
	args = append(args, prompt)

	started := time.Now()
	compactLog.Logger().Info("compact.summarize.spawned",
		"component", "compact",
		"subcomponent", "summarize",
		"binary", binary,
		"prompt_bytes", len(prompt),
		"timeout_s", int(timeout.Seconds()),
	)

	cmd := exec.CommandContext(callCtx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		tail := stderr.String()
		if len(tail) > 1024 {
			tail = tail[len(tail)-1024:]
		}
		compactLog.Logger().Warn("compact.summarize.failed",
			"component", "compact",
			"subcomponent", "summarize",
			"duration_ms", time.Since(started).Milliseconds(),
			"stderr_tail", tail,
			"err", err,
		)
		return "", fmt.Errorf("summarize: %w", err)
	}
	summary := strings.TrimSpace(stdout.String())
	compactLog.Logger().Info("compact.summarize.completed",
		"component", "compact",
		"subcomponent", "summarize",
		"duration_ms", time.Since(started).Milliseconds(),
		"summary_bytes", len(summary),
	)
	return summary, nil
}

// renderDroppedForSummary emits a plain text view of just the
// portions the current opts will drop from the slice. This is what we
// feed to the summarizer.
func renderDroppedForSummary(slice *Slice, opts SynthOptions) string {
	var sb strings.Builder

	if len(opts.DroppedChatEntries) > 0 {
		sb.WriteString("# Dropped chat turns\n\n")
		for ei, e := range slice.PostBoundary {
			if !opts.DroppedChatEntries[ei] {
				continue
			}
			if e.Type != "user" && e.Type != "assistant" {
				continue
			}
			text := chatTextFrom(e, SynthOptions{DropThinking: true})
			if text == "" {
				continue
			}
			fmt.Fprintf(&sb, "## %s (%s)\n\n%s\n\n", titleCaseASCII(e.Type), e.Timestamp.UTC().Format(time.RFC3339), text)
		}
	}

	if len(opts.DroppedSummaryChunks) > 0 {
		indexes := make([]int, 0, len(opts.DroppedSummaryChunks))
		for ei := range opts.DroppedSummaryChunks {
			indexes = append(indexes, ei)
		}
		sort.Ints(indexes)
		wroteHeader := false
		for _, ei := range indexes {
			dropped := opts.DroppedSummaryChunks[ei]
			if len(dropped) == 0 {
				continue
			}
			if ei < 0 || ei >= len(slice.PostBoundary) {
				continue
			}
			summary, ok := parseSyntheticSummary(slice.PostBoundary[ei])
			if !ok {
				continue
			}
			text := summary.DroppedText(dropped)
			if strings.TrimSpace(text) == "" {
				continue
			}
			if !wroteHeader {
				sb.WriteString("# Dropped prior compact summary chunks\n\n")
				wroteHeader = true
			}
			sb.WriteString(text)
			sb.WriteString("\n\n")
		}
	}

	var droppedTools []ContentBlock
	var droppedToolEntries []Entry
	for _, e := range slice.PostBoundary {
		if e.Type != "assistant" {
			continue
		}
		for _, b := range e.Content {
			if b.Type != "tool_use" {
				continue
			}
			detail := opts.ToolDefault
			if override, ok := opts.ToolDetailOverride[b.ToolUseID]; ok {
				detail = override
			}
			if detail == ToolDetailDrop {
				droppedTools = append(droppedTools, b)
				droppedToolEntries = append(droppedToolEntries, e)
			}
		}
	}
	if len(droppedTools) > 0 {
		sb.WriteString("# Dropped tool calls\n\n")
		for i, b := range droppedTools {
			e := droppedToolEntries[i]
			args := summarizeToolArgs(b)
			fmt.Fprintf(&sb, "- %s %s(%s)\n", e.Timestamp.UTC().Format(time.RFC3339), b.ToolName, args)
		}
		sb.WriteString("\n")
	}

	return sb.String()
}

// titleCaseASCII upper-cases the first byte of an ASCII identifier such as
// "user" or "assistant". It exists to avoid the deprecated strings.Title
// (deprecated since Go 1.18 due to Unicode word-boundary issues) for inputs
// that are guaranteed lowercase ASCII.
func titleCaseASCII(s string) string {
	if s == "" {
		return s
	}
	c := s[0]
	if c >= 'a' && c <= 'z' {
		return string(c-32) + s[1:]
	}
	return s
}
