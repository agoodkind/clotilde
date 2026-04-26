package anthropicbackend

import (
	"log/slog"

	"goodkind.io/clyde/internal/adapter/anthropic"
)

// MicrocompactClearedMessage is the sentinel text we write into aged
// tool_result bodies. Must match Claude Code's
// TIME_BASED_MC_CLEARED_MESSAGE so the server treats the prompt
// identically to what the CLI would send.
const MicrocompactClearedMessage = "[Old tool result content cleared]"

// DefaultMicrocompactKeepRecent mirrors Claude's GrowthBook default for
// time-based MC. Older compactable tool_results are cleared; newer ones
// stay verbatim.
const DefaultMicrocompactKeepRecent = 15

// compactableTools is the set of tool names whose outputs are large
// and expensive to keep around. Matches Claude's COMPACTABLE_TOOLS
// set in microCompact.ts. Tool names vary by client (Cursor emits
// its own canonical names for file/shell/search operations); we
// match on common spellings rather than internal constants so
// non-Claude-Code clients benefit too. Membership is case-sensitive.
var compactableTools = map[string]bool{
	// Claude Code canonical names
	"Read":      true,
	"Edit":      true,
	"Write":     true,
	"Bash":      true,
	"Grep":      true,
	"Glob":      true,
	"WebFetch":  true,
	"WebSearch": true,
	// Cursor / OpenAI-function-style spellings (snake_case variants)
	"read_file":        true,
	"edit_file":        true,
	"write_file":       true,
	"run_terminal":     true,
	"run_terminal_cmd": true,
	"grep_search":      true,
	"codebase_search":  true,
	"file_search":      true,
	"web_search":       true,
	"fetch":            true,
}

// ApplyMicrocompact rewrites aged tool_result bodies to the cleared
// placeholder so the transcript carries less data across turns. The
// most recent keepRecent compactable tool_use IDs are preserved
// verbatim; older ones have their paired tool_result content
// replaced.
//
// Operates in place on msgs. Returns the number of tool_results that
// were cleared and a rough estimate of the bytes saved for logging.
// Idempotent: when a tool_result already carries the sentinel text,
// the walk skips it and reports no additional clearing.
//
// keepRecent <= 0 is normalized to DefaultMicrocompactKeepRecent.
func ApplyMicrocompact(msgs []anthropic.Message, keepRecent int) (cleared int, bytesSaved int) {
	if keepRecent <= 0 {
		keepRecent = DefaultMicrocompactKeepRecent
	}
	ids := collectCompactableToolIDs(msgs)
	if len(ids) <= keepRecent {
		return 0, 0
	}
	// The last keepRecent ids stay. Everything before them is cleared.
	clearSet := make(map[string]bool, len(ids)-keepRecent)
	for _, id := range ids[:len(ids)-keepRecent] {
		clearSet[id] = true
	}
	for mi := range msgs {
		if msgs[mi].Role != "user" {
			continue
		}
		for bi := range msgs[mi].Content {
			b := &msgs[mi].Content[bi]
			if b.Type != "tool_result" {
				continue
			}
			if !clearSet[b.ToolUseID] {
				continue
			}
			if b.Content == MicrocompactClearedMessage {
				continue
			}
			bytesSaved += len(b.Content)
			b.Content = MicrocompactClearedMessage
			cleared++
		}
	}
	return cleared, bytesSaved
}

// collectCompactableToolIDs walks assistant messages in order and
// returns every tool_use id whose tool name is in compactableTools.
func collectCompactableToolIDs(msgs []anthropic.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role != "assistant" {
			continue
		}
		for _, b := range m.Content {
			if b.Type != "tool_use" {
				continue
			}
			if !compactableTools[b.Name] {
				continue
			}
			if b.ID == "" {
				continue
			}
			out = append(out, b.ID)
		}
	}
	return out
}

// LogMicrocompact emits a one-shot event when microcompact clears at
// least one tool_result. Quiet no-op otherwise so the normal fast
// path does not clutter the log.
func LogMicrocompact(log *slog.Logger, reqID, alias string, cleared, bytesSaved, keepRecent int) {
	if log == nil || cleared == 0 {
		return
	}
	log.Info("adapter.microcompact.applied",
		"request_id", reqID,
		"alias", alias,
		"tools_cleared", cleared,
		"bytes_saved", bytesSaved,
		"keep_recent", keepRecent,
	)
}
