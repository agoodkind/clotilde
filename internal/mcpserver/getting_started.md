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
Search a session's conversation history for where a topic was discussed.
- session_name (required): which session to search
- query (required): natural language description of what to find
- depth (optional): controls speed vs accuracy

**Search depth levels (always start at quick, escalate only if needed):**
- `quick` (default, ~20s): embedding similarity only, no LLM. Use this first.
- `normal` (~4min): embedding filter + LLM sweep. Use when quick results are vague or missing.
- `deep` (~5min): adds a reranker pass for better precision. Use for important lookups.
- `extra-deep` (20min+): adds large model verification. Only use when explicitly asked.

### clotilde_get_context
Get context around a specific message index in a session.
- session_name (required): which session to read
- index (required): message index to center on
- before (optional): messages to include before (default 3)
- after (optional): messages to include after (default 3)

### clotilde_analyze_results
Run an LLM synthesis pass over the results from a previous search without re-running it.
- result_id (required): the result_id returned by clotilde_search_conversation
- prompt (required): what to extract or analyze (e.g. "List every frustration instance with timestamp and verbatim quote")

Results are cached in memory for the lifetime of the MCP server process.

## Typical workflow
1. Call clotilde_list_sessions to see what sessions exist
2. Call clotilde_search_conversation with depth=quick to find a specific discussion
3. If quick results are insufficient, retry with depth=normal
4. Call clotilde_analyze_results with the result_id to synthesize or extract structured data from matches
5. Call clotilde_get_context to expand around a relevant message index
6. Call clotilde_get_conversation with last_n to get recent context from another session

This lets you search your own history, cross-reference other sessions, and recall past discussions.
