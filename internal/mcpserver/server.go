// Package mcpserver exposes clotilde session tools as an MCP server (stdio transport).
// Claude Code connects to this process and can search/list/view sessions as tools.
package mcpserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/search"
	"github.com/fgrehm/clotilde/internal/session"
	"github.com/fgrehm/clotilde/internal/transcript"
	"github.com/fgrehm/clotilde/internal/util"
)

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	s := server.NewMCPServer("clotilde", "0.13.0-dev")

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
		mcp.NewTool("clotilde_search_conversation",
			mcp.WithDescription("Search a session's conversation history using an LLM to find where a topic was discussed. Returns matching messages."),
			mcp.WithString("session_name", mcp.Required(), mcp.Description("Session name to search.")),
			mcp.WithString("query", mcp.Required(), mcp.Description("What to search for (natural language).")),
		),
		handleSearchConversation,
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

	cfg, _ := config.LoadGlobalOrDefault()
	results, err := search.Search(ctx, messages, query, cfg.Search)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Search failed: %v", err)), nil
	}

	if len(results) == 0 {
		return mcp.NewToolResultText("No matching messages found."), nil
	}

	var sb strings.Builder
	for _, r := range results {
		if r.Summary != "" {
			fmt.Fprintf(&sb, "**Found:** %s\n\n", r.Summary)
		}
		sb.WriteString(transcript.RenderPlainText(r.Messages))
		sb.WriteString("---\n\n")
	}
	return mcp.NewToolResultText(sb.String()), nil
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
