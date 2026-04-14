// Package mcpserver exposes clotilde session tools as an MCP server (stdio transport).
// Claude Code connects to this process and can search/list/view sessions as tools.
package mcpserver

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/fgrehm/clotilde/internal/audit"
	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/search"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// resultCache stores search results in memory so callers can reference them by ID
// for follow-up analysis without re-running the search.
type cachedResult struct {
	SessionName string
	Messages    []transcript.Message // all matched messages, flattened
	Results     []search.Result      // original results with summaries
	CreatedAt   time.Time
}

var resultCache sync.Map // map[string]*cachedResult

//go:embed getting_started.md
var gettingStartedPrompt string

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	log, cleanup := audit.NewLogger("mcp")
	defer cleanup()
	slog.SetDefault(log)

	s := server.NewMCPServer("clotilde", "0.13.0-dev")

	// --- Prompts (slash commands) ---

	s.AddPrompt(
		mcp.Prompt{
			Name:        "clotilde",
			Description: "Get started with clotilde session management. Lists available tools and explains how to use them.",
		},
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: "clotilde session management",
				Messages: []mcp.PromptMessage{
					{
						Role:    mcp.RoleUser,
						Content: mcp.NewTextContent(gettingStartedPrompt),
					},
				},
			}, nil
		},
	)

	// --- Tools ---

	s.AddTool(
		mcp.NewTool("clotilde_list_sessions",
			mcp.WithDescription("List all clotilde sessions with their names, workspaces, models, and context. Use this to find sessions before searching."),
			mcp.WithBoolean("all", mcp.Description("Show all sessions across all workspaces (default: current workspace only).")),
		),
		handleListSessions,
	)

	s.AddTool(
		mcp.NewTool("clotilde_get_conversation",
			mcp.WithDescription("Get the plain text conversation from a session. Returns user and assistant messages without tool call details."),
			mcp.WithString("session_name", mcp.Required(), mcp.Description("Session name to retrieve.")),
			mcp.WithNumber("last_n", mcp.Description("Only return the last N messages (default: all).")),
		),
		handleGetConversation,
	)

	s.AddTool(
		mcp.NewTool("clotilde_get_context",
			mcp.WithDescription("Get messages around a specific point in a session's conversation. Use after search to expand context around a match. Provide either a timestamp or message_index to center on."),
			mcp.WithString("session_name", mcp.Required(), mcp.Description("Session name.")),
			mcp.WithString("timestamp", mcp.Description("ISO timestamp to center on (e.g. '2026-04-12 15:04'). Finds nearest message.")),
			mcp.WithNumber("message_index", mcp.Description("0-based message index to center on.")),
			mcp.WithNumber("before", mcp.Description("Number of messages to include before the center (default: 5).")),
			mcp.WithNumber("after", mcp.Description("Number of messages to include after the center (default: 5).")),
		),
		handleGetContext,
	)

	s.AddTool(
		mcp.NewTool("clotilde_search_conversation",
			mcp.WithDescription("Search a session's conversation history for where a topic was discussed. Returns matching messages with context and a result_id for follow-up analysis. Always start with 'quick' (embedding only, ~20s). Escalate only when quick results are insufficient."),
			mcp.WithString("session_name", mcp.Required(), mcp.Description("Session name to search.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("What to search for (natural language).")),
			mcp.WithString("depth", mcp.Description("Search depth: 'quick' (embedding only, ~20s, default), 'normal' (+ LLM sweep, ~4min), 'deep' (+ rerank, ~5min), 'extra-deep' (+ large model, 20min+, warns before running).")),
		),
		handleSearchConversation,
	)

	s.AddTool(
		mcp.NewTool("clotilde_analyze_results",
			mcp.WithDescription("Run an LLM analysis pass over the results from a previous clotilde_search_conversation call. Use the result_id returned by search. The LLM will synthesize, extract, or summarize based on your prompt. Avoids re-running the search."),
			mcp.WithString("result_id", mcp.Required(), mcp.Description("The result_id returned by clotilde_search_conversation.")),
			mcp.WithString("prompt", mcp.Required(), mcp.Description("What to extract or analyze from the search results (e.g. 'List every frustration instance with timestamp and verbatim quote').")),
		),
		handleAnalyzeResults,
	)

	return server.ServeStdio(s)
}

func handleListSessions(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Failed to open session store: %v", err)), nil
	}

	showAll := req.GetBool("all", false)

	var sessions []*session.Session
	if showAll {
		sessions, err = store.List()
	} else {
		workspaceRoot, _ := config.FindProjectRoot()
		if workspaceRoot != "" {
			sessions, err = store.ListForWorkspace(workspaceRoot)
		} else {
			sessions, err = store.List()
		}
	}
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Failed to list sessions: %v", err)), nil
	}

	if len(sessions) == 0 {
		return mcp.NewToolResultText("No sessions found."), nil
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "%d sessions:\n\n", len(sessions))
	for _, sess := range sessions {
		fmt.Fprintf(&sb, "- **%s**", sess.Name)
		if sess.Metadata.WorkspaceRoot != "" {
			fmt.Fprintf(&sb, " (%s)", shortPath(sess.Metadata.WorkspaceRoot))
		}
		if sess.Metadata.Context != "" {
			fmt.Fprintf(&sb, " - %s", sess.Metadata.Context)
		}
		sb.WriteString("\n")
	}
	return mcp.NewToolResultText(sb.String()), nil
}

func handleGetContext(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("session_name", "")
	if name == "" {
		return mcp.NewToolResultText("session_name is required"), nil
	}

	messages, err := loadMessages(name)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Failed to load conversation: %v", err)), nil
	}
	if len(messages) == 0 {
		return mcp.NewToolResultText("No conversation messages found."), nil
	}

	before := int(req.GetFloat("before", 5))
	after := int(req.GetFloat("after", 5))

	// Find center point: by timestamp or by index
	center := -1
	if ts := req.GetString("timestamp", ""); ts != "" {
		center = findNearestMessage(messages, ts)
	}
	if center < 0 {
		idx := int(req.GetFloat("message_index", -1))
		if idx >= 0 && idx < len(messages) {
			center = idx
		}
	}
	if center < 0 {
		return mcp.NewToolResultText("Provide either timestamp or message_index to center on."), nil
	}

	// Extract window
	start := center - before
	if start < 0 {
		start = 0
	}
	end := center + after + 1
	if end > len(messages) {
		end = len(messages)
	}

	window := messages[start:end]
	text := fmt.Sprintf("Messages %d-%d of %d (centered on %d):\n\n%s",
		start, end-1, len(messages), center, transcript.RenderPlainText(window))
	return mcp.NewToolResultText(text), nil
}

// findNearestMessage finds the message closest to the given timestamp string.
func findNearestMessage(messages []transcript.Message, ts string) int {
	// Try common formats
	var target time.Time
	for _, layout := range []string{
		"2006-01-02 15:04",
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05",
	} {
		if t, err := time.Parse(layout, ts); err == nil {
			target = t
			break
		}
	}
	if target.IsZero() {
		return -1
	}

	best := -1
	bestDiff := time.Duration(1<<63 - 1)
	for i, m := range messages {
		diff := m.Timestamp.Sub(target)
		if diff < 0 {
			diff = -diff
		}
		if diff < bestDiff {
			bestDiff = diff
			best = i
		}
	}
	return best
}

func handleGetConversation(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("session_name", "")
	if name == "" {
		return mcp.NewToolResultText("session_name is required"), nil
	}

	lastN := int(req.GetFloat("last_n", 0))

	messages, err := loadMessages(name)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Failed to load conversation: %v", err)), nil
	}

	if lastN > 0 && lastN < len(messages) {
		messages = messages[len(messages)-lastN:]
	}

	text := transcript.RenderPlainText(messages)
	if len(text) == 0 {
		return mcp.NewToolResultText("No conversation messages found."), nil
	}
	return mcp.NewToolResultText(text), nil
}

func handleSearchConversation(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name := req.GetString("session_name", "")
	query := req.GetString("query", "")
	if name == "" || query == "" {
		return mcp.NewToolResultText("session_name and query are required"), nil
	}

	messages, err := loadMessages(name)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Failed to load conversation: %v", err)), nil
	}
	if len(messages) == 0 {
		return mcp.NewToolResultText("No conversation messages found."), nil
	}

	depth := req.GetString("depth", "quick")
	cfg, _ := config.LoadGlobalOrDefault()
	results, err := search.SearchWithDepth(ctx, messages, query, cfg.Search, depth)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Search failed: %v", err)), nil
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("No matching messages found."), nil
	}

	// Store matched messages in cache so the caller can run follow-up analysis.
	resultID := util.GenerateUUID()
	var flatMessages []transcript.Message
	for _, r := range results {
		flatMessages = append(flatMessages, r.Messages...)
	}
	resultCache.Store(resultID, &cachedResult{
		SessionName: name,
		Messages:    flatMessages,
		Results:     results,
		CreatedAt:   time.Now(),
	})

	// Build a UUID-to-index map so we can show global message indices.
	idxMap := make(map[string]int, len(messages))
	for i, m := range messages {
		if m.UUID != "" {
			idxMap[m.UUID] = i
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "result_id: %s (pass to clotilde_analyze_results for follow-up analysis)\n\n", resultID)
	sb.WriteString(fmt.Sprintf("Use clotilde_get_context with session_name=%q and message_index=N to expand around any result.\n\n", name))
	for _, r := range results {
		if r.Summary != "" {
			fmt.Fprintf(&sb, "**Found:** %s\n\n", r.Summary)
		}
		for _, m := range r.Messages {
			idx, ok := idxMap[m.UUID]
			if !ok {
				idx = -1
			}
			ts := m.Timestamp.Format("2006-01-02 15:04")
			role := "User"
			if m.Role == "assistant" {
				role = "Assistant"
			}
			if idx >= 0 {
				fmt.Fprintf(&sb, "[#%d][%s] %s:\n", idx, ts, role)
			} else {
				fmt.Fprintf(&sb, "[%s] %s:\n", ts, role)
			}
			if m.Text != "" {
				sb.WriteString(m.Text)
				sb.WriteString("\n")
			}
			if m.HasTools {
				fmt.Fprintf(&sb, "  [used: %s]\n", strings.Join(m.ToolNames(), ", "))
			}
			sb.WriteString("\n")
		}
		sb.WriteString("---\n\n")
	}
	return mcp.NewToolResultText(sb.String()), nil
}

func handleAnalyzeResults(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	resultID := req.GetString("result_id", "")
	prompt := req.GetString("prompt", "")
	if resultID == "" || prompt == "" {
		return mcp.NewToolResultText("result_id and prompt are required"), nil
	}

	val, ok := resultCache.Load(resultID)
	if !ok {
		return mcp.NewToolResultText(fmt.Sprintf("result_id %q not found. Results are cached for the lifetime of this MCP server process only.", resultID)), nil
	}
	cached := val.(*cachedResult)

	if len(cached.Messages) == 0 {
		return mcp.NewToolResultText("Cached result has no messages."), nil
	}

	// Build conversation text from all cached messages.
	var convText strings.Builder
	for _, m := range cached.Messages {
		ts := m.Timestamp.Format("2006-01-02 15:04")
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		fmt.Fprintf(&convText, "[%s] %s:\n%s\n\n", ts, role, m.Text)
	}

	fullPrompt := fmt.Sprintf("%s\n\nCONVERSATION EXCERPTS from session %q:\n\n%s",
		prompt, cached.SessionName, convText.String())

	cfg, _ := config.LoadGlobalOrDefault()
	pipeline := cfg.Search.Local.Pipeline
	model := cfg.Search.Local.Model
	if len(pipeline) > 0 {
		model = pipeline[0].Model
	}
	if model == "" {
		model = "qwen2.5-coder-32b"
	}

	client := search.NewClientForModel(cfg.Search, model)
	resp, err := client.Complete(ctx, fullPrompt)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Analysis failed: %v", err)), nil
	}

	slog.Info("analyze_results complete",
		"result_id", resultID,
		"session", cached.SessionName,
		"cached_messages", len(cached.Messages),
		"response_chars", len(resp),
	)

	return mcp.NewToolResultText(resp), nil
}

// loadMessages loads all parsed messages for a session by name.
func loadMessages(name string) ([]transcript.Message, error) {
	store, err := session.NewGlobalFileStore()
	if err != nil {
		return nil, err
	}
	sess, err := store.Get(name)
	if err != nil {
		return nil, fmt.Errorf("session '%s' not found", name)
	}

	homeDir, err := util.HomeDir()
	if err != nil {
		return nil, err
	}

	root := sess.Metadata.WorkspaceRoot
	if root == "" {
		root, _ = config.FindProjectRoot()
	}
	clotildeRoot := root + "/.claude/clotilde"

	var paths []string
	for _, prevID := range sess.Metadata.PreviousSessionIDs {
		if prevID != "" {
			paths = append(paths, claudeTranscriptPath(homeDir, clotildeRoot, prevID))
		}
	}
	current := sess.Metadata.TranscriptPath
	if current == "" && sess.Metadata.SessionID != "" {
		current = claudeTranscriptPath(homeDir, clotildeRoot, sess.Metadata.SessionID)
	}
	if current != "" {
		paths = append(paths, current)
	}

	var allMessages []transcript.Message
	for _, path := range paths {
		f, openErr := os.Open(path)
		if openErr != nil {
			continue
		}
		msgs, parseErr := transcript.Parse(f)
		_ = f.Close()
		if parseErr != nil {
			continue
		}
		allMessages = append(allMessages, msgs...)
	}
	return allMessages, nil
}

func claudeTranscriptPath(homeDir, clotildeRoot, sessionID string) string {
	projectRoot := clotildeRoot
	if strings.HasSuffix(projectRoot, "/.claude/clotilde") {
		projectRoot = strings.TrimSuffix(projectRoot, "/.claude/clotilde")
	}
	encoded := strings.ReplaceAll(projectRoot, "/", "-")
	encoded = strings.ReplaceAll(encoded, ".", "-")
	return homeDir + "/.claude/projects/" + encoded + "/" + sessionID + ".jsonl"
}

func shortPath(root string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return root
	}
	if root == home {
		return "~"
	}
	if strings.HasPrefix(root, home+"/") {
		return "~/" + root[len(home)+1:]
	}
	return root
}
