You have access to clotilde, a session management layer for Claude Code.

Available tools:

### clotilde_list_sessions
List all sessions with names, workspaces, models, and context summaries.
- Pass all=true to see every session across all workspaces.

### clotilde_get_conversation
Get the plain text conversation from any session (user + assistant messages, no tool call noise).
- session_name (required): which session to read
- last_n (optional): only get the last N messages

### clotilde_search_conversation
Search a session's conversation history using an LLM to find where a topic was discussed.
- session_name (required): which session to search
- query (required): natural language description of what to find

## Typical workflow
1. Call clotilde_list_sessions to see what sessions exist
2. Call clotilde_search_conversation to find a specific discussion
3. Call clotilde_get_conversation with last_n to get recent context from another session

This lets you search your own history, cross-reference other sessions, and recall past discussions.
