# CHANGELOG

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **Per session `--remote-control` flag.** New CLI flags `--remote-control` and `--no-remote-control` on `clotilde start`, `resume`, `fork`, and `incognito` opt the session into Claude Code's `--remote-control` bridge. The value persists in the session's `settings.json` so subsequent invocations inherit it. Profiles can set the flag globally via a new `RemoteControl *bool` field. The TUI options popup gains "Enable remote control" / "Disable remote control" entries that route through a new daemon RPC (`UpdateSessionSettings`) so concurrent dashboards see the change immediately.
- **Bridge URL surface.** A new daemon-side `bridge.Watcher` runs `fsnotify` on `~/.claude/sessions/` and tracks every active bridge session by Claude session UUID. The dashboard renders an `RC` badge column between MODEL and MSGS, a "Remote ctrl" row in the details pane, an `RC×N` badge on the status bar, and "Open bridge in browser" / "Copy bridge URL" entries in the options popup. Two new CLI commands round it out: `clotilde bridge ls` lists active bridges and `clotilde bridge open <session>` jumps the system browser to the URL.
- **Live "Sidecar" tab.** A third tab on the top bar (next to Sessions and Settings) shows a live transcript stream for any session pinned with `S`. The daemon owns one `transcript.Tailer` per active session and fans events out via the new `TailTranscript` server-side streaming RPC. The sidecar renders a header with the session name and bridge URL, an auto-following message buffer, and a single-line input that injects user text back into claude.
- **Inject channel.** Sessions launched with `--remote-control` now run inside a pty (via `github.com/creack/pty`) so clotilde can route external input into claude's stdin. The wrapper opens a per-session Unix socket at `$XDG_RUNTIME_DIR/clotilde/inject/<sessionId>.sock`. The new daemon `SendToSession` RPC dials the socket and forwards bytes. Two consumers exist today: the Sidecar input and the new `clotilde send <session> "text"` CLI command.
- **Daemon settings authority.** A new `UpdateGlobalSettings` RPC writes the global config TOML through `internal/config.SaveGlobal`. The Settings tab grows a `G` shortcut that toggles the global remote control default and a status row showing the current value.
- **New `--strip-thinking` flag on `clotilde compact`.** Previously thinking blocks were stripped as a side effect of `--strip-tool-results`. They are now controlled by an independent flag. `--strip-tool-results` only touches `tool_result` blocks. `--strip-thinking` only removes assistant thinking blocks. The two flags compose freely.
- **Automatic pre-compact transcript backups.** Every destructive `clotilde compact` run now copies the current transcript into `$XDG_DATA_HOME/clotilde/backups/<session>/` before applying any change. Filenames use a UTC timestamp with millisecond precision so successive backups never collide. The most recent 20 backups per session are retained; older ones are pruned automatically. Dry-run compacts skip the backup step.

### Fixed

- **Compact no longer writes `"content":null` into transcripts.** When the compact "strip" option removed the only block of an assistant message (typically a lone thinking block), the resulting content array was marshalled as `null` instead of `[]`. Claude Code's transcript loader then crashed on resume with `null is not an object (evaluating 'H.message.content.length')`. The empty array is now emitted correctly, and a regression test (`TestStripContent_EmptyAfterThinking`) guards against it.

### Changed

**TUI rewrite - raw tcell (removes tview)**

- **Full rewrite of the main `clotilde` TUI on top of raw tcell.** The tview dependency is removed entirely. Every widget (Table, TextBox, StatusBar, Modal, Input, DetailsView) is implemented directly on tcell primitives in `internal/ui/tcell_*.go`.
- **Fixes mouse clicks on session rows.** Clicks now hit-test directly against widget rectangles (no Frame wrapper, no InRect delegation chain). Single click opens the details pane. Double click resumes the session.
- **Fixes unresponsive TUI after terminal tab switch.** `screen.EnableFocus()` is now called, and `tcell.EventFocus` triggers a full `screen.Sync()` on refocus.
- **Ctrl+C always quits.** Global key handling runs before any widget and cannot be blocked by focus routing.
- Legacy BubbleTea-based subcommand helpers (`picker`, `viewer`, `confirm`, `dashboard`, `table`, `input`) remain in place for subcommands such as `clotilde delete <name>` and `clotilde resume` (when called without TTY). They are unaffected by the rewrite.

### Added

**TUI rewrite - tview/tcell**

- **Full tview/tcell TUI rewrite**: Replaced BubbleTea dashboard with native tview application for improved terminal compatibility and rendering performance.
- **Session table as primary view**: Interactive sortable session table with clickable column headers (up to 5 keys). Displays NAME, MODEL, DIR, CREATED, and LAST USED columns with auto-sizing and proportional widths.
- **Details pane (bottom panel)**: Two-column layout with left stats panel (session metadata: UUID, workspace, transcript size, message count) and right messages panel (scrollable conversation preview).
- **nvim-style status bar**: Bottom status line with BROWSE/DETAIL/SEARCH/COMPACT mode badges, scroll position percentage, and contextual help text.
- **Spacebar opens details with focus**: Spacebar opens the details pane with automatic focus, Enter returns focus to session table and resumes session. Arrow keys move selection without opening details.
- **Double-click to resume**: Double-clicking a session row immediately resumes that session. Mouse scroll support for navigating tables and panes.
- **Search form overlay**: s key opens a full-screen search form overlay with session picker, query input, and depth selector (quick/normal/deep/extra-deep, navigated with arrow keys).
- **Compact form overlay**: c key opens a compact form overlay with boundary percentage input, strip options (tool results, large blocks), and dry-run toggle before executing compaction.
- **Delete confirmation modal**: d key opens a confirmation modal for session deletion with esc/no and enter/yes controls.
- **Conversation viewer overlay**: v key opens a full-screen overlay displaying the complete session conversation with scrolling support.
- **Background model extraction**: SESSION table populates MODEL column asynchronously by parsing transcripts in background goroutines.
- **256-color terminal palette**: Catppuccin Mocha color scheme using tcell's 256-color terminal palette for broad compatibility (vs BubbleTea's Lipgloss color system).

**Session management**

- **Session display names**: Sessions now support a `displayName` metadata field shown in the TUI instead of the raw session ID (e.g. `opnsense-bgp-cutover` instead of `configs-6d383f1d`). The original name is unchanged and still used for `resume`, `fork`, `delete`, etc. Dashboard, picker, list table, and inspect all show the display name; picker filter matches both raw name and display name.
- **`clotilde auto-name`**: Generates human-readable kebab-case display names for sessions via `claude haiku` using conversation content. Supports `--all` (batch all unnamed), `--force` (regenerate existing), `--dry-run` (preview without saving), and `--model` flags.
- **`clotilde adopt`**: Imports sessions from legacy per-project `.claude/clotilde/` stores into the global store. Also scans for any untracked session UUIDs and creates metadata for them. Running `clotilde resume <uuid>` auto-adopts sessions not yet registered.
- **`clotilde exec`**: Runs a bare `claude` invocation with model isolation via the daemon, without session tracking overhead.
- **Smart resume lookup**: `clotilde resume` tries four strategies in order: exact name match, UUID match (with auto-adopt), display name match, and substring fuzzy search. If multiple sessions match a substring, a TUI picker is shown.
- **Fall-through for unknown session names**: When `clotilde resume <name>` finds no matching session, it forwards the arguments to `claude` directly, making clotilde a drop-in wrapper for any claude invocation.
- **CWD restore on resume**: Sessions remember the working directory where they were created. `clotilde resume <name>` restores Claude to the original directory regardless of where the resume command is run from. Applies to `start`, `fork`, and `incognito` sessions.
- **`--add-dir` injection**: `clotilde resume` automatically adds the session's workspace root via `--add-dir` so Claude can read files there even when resumed from a different directory.
- **Centralized global session storage**: Session metadata is now stored in a single global directory (`$XDG_DATA_HOME/clotilde/sessions/`, defaulting to `~/.local/share/clotilde/sessions/`) instead of per-project `.claude/clotilde/` directories. Sessions track their originating workspace via a `workspaceRoot` metadata field.
- **Exit instructions**: After a session exits, clotilde prints a short resume hint (`clotilde resume <name>`) so the session name is visible in the terminal buffer.
- **`clotilde compact` command**: Manipulates session transcript boundaries with `--move-boundary`, `--remove-last-boundary`, `--strip-tool-results`, `--strip-large`, `--keep-last`, and `--dry-run` flags for fine-grained transcript cleanup.
- **Token estimation in compact**: Uses tiktoken cl100k_base with 1.15x multiplier to estimate token usage before and after compaction.
- **Before/after preview in compact**: Shows entry counts, token estimates, and first 5 user messages to help validate compaction changes.
- **`store.Resolve()` method**: Unified 4-tier session lookup (exact name, UUID, display name, substring) used across commands and MCP tools.
- **`store.Rename()` method**: Renames sessions by moving directory, updating metadata, and updating fork parent references.
- **`bench-embed` command**: Compares embedding model performance across different embedding backends.

**Transcript parsing**

- **Array-format user content blocks**: Transcript parser now supports user content blocks in array format, recovering 31.6% of previously invisible messages.
- **Unit tests for transcript parser**: Tests cover string, array, tool_result, mixed, and system tag stripping cases.

**Embedding models**

- **Snowflake Arctic Embed L**: Added as configurable embedding model option with better score separation than nomic-embed-text.

**Context and LLM**

- **Auto-populate session context via LLM**: When a session exits, clotilde fires a background gRPC call to the daemon. The daemon extracts the 5 most recent messages from the transcript and asks `claude sonnet` for a one-sentence summary (under 15 words), which is stored in `metadata.context` and shown in the list preview and TUI.
- **TOML config support**: `~/.config/clotilde/config.toml` (respects `$XDG_CONFIG_HOME`) replaces the JSON config format. Supports a `[defaults]` section for global flag defaults (e.g. `remote-control = true`).

**TUI and display**

- **Rich preview pane in `clotilde list`**: The interactive list shows a right-hand preview panel with UUID, workspace, model, transcript size, message count, and the most recent user message for any selected session.
- **Richer list table**: `clotilde list` now shows `DIR` (shortened workspace path) and `CREATED` columns alongside `NAME`, `MODEL`, and `LAST USED`.
- **TUI conversation viewer**: Dashboard "View conversation" action opens a scrollable BubbleTea viewport with the full session transcript rendered as plain text (user + assistant messages, no tool call noise).
- **Search form TUI**: Dashboard "Search conversation" opens a full-screen form with three fields: session (picker), query (text input), and depth selector (quick / normal / deep / extra-deep, navigated with left/right arrows). Replaces the previous sequential picker-then-input flow.
- **Return to dashboard after session exit**: When a session exits (`/exit`, Ctrl+D), clotilde returns to the TUI dashboard instead of dropping to the shell. A "Return to session" action at the top lets the user resume immediately.
- **Dashboard "Auto-name sessions" action**: Shows a confirmation dialog then generates display names for all unnamed sessions in the workspace via `claude haiku`.
- **`clotilde inspect` shows display name**: `clotilde inspect <session>` prints the `Display Name` field when one is set, and shows it above the raw session ID in the rich list preview.

**Search pipeline**

- **`clotilde search <name> <query>`**: CLI command to search a session's conversation history. Uses the configured LLM backend to find where a topic was discussed and returns the matching messages.
- **Local LLM support**: Search uses an OpenAI-compatible endpoint (LM Studio, Ollama, etc.) when `search.backend = "local"` in config. Configurable URL, model, and sampling parameters.
- **4-tier search depth**: `--depth quick` (embedding similarity only, ~20s), `--depth normal` (embedding + LLM chunk sweep, ~4min), `--depth deep` (adds LLM reranker pass, ~5min), `--depth extra-deep` (adds large model verification, 20min+). Default is `quick`.
- **Embedding pre-filter**: Before LLM search, chunks are pre-filtered by cosine similarity against the query embedding (using `nomic-embed-text`). Threshold configurable via `search.local.embedding_threshold` (default 0.5).
- **LLM reranker pass**: At `deep` depth, a second LLM pass scores candidate chunks by relevance and de-duplicates, improving precision over the initial sweep.
- **Memory-aware model management**: The search pipeline calls the `lms` CLI to explicitly load and unload models around each pipeline phase, avoiding LM Studio's memory eviction from evicting the embedding model mid-search.
- **Auto model swap between pipeline layers**: Each depth tier uses a different model appropriate to the task. The pipeline swaps models via `lms load`/`lms unload` between phases.
- **Configurable chunk size and concurrency**: `search.local.chunk_size` (default 4000 chars) and `search.max_concurrency` (default 5) are tunable via TOML.
- **Per-phase timing instrumentation**: Each phase of the search pipeline logs start and end times via `slog`, making it easy to identify bottlenecks. Audit logs are written to `~/.local/share/clotilde/audit/`.
- **Configurable LLM sampling params**: `temperature`, `top_p`, and `frequency_penalty` are configurable per backend in `config.toml`.

**MCP server**

- **`clotilde mcp`**: Starts a Model Context Protocol server exposing session tools to any MCP-capable client (Claude Code, Cursor, etc.). Tools: `clotilde_list_sessions`, `clotilde_get_conversation`, `clotilde_search_conversation`, `clotilde_get_context`, `clotilde_analyze_results`.
- **`clotilde_search_conversation`**: Searches a session's conversation history from inside another Claude session. Returns a `result_id` along with matching messages. Supports the same depth tiers as `clotilde search`.
- **`clotilde_analyze_results`**: Runs an LLM synthesis pass over the result set from a previous `clotilde_search_conversation` call without re-running the search. Useful for extracting structured data or summaries from a large result set.
- **`clotilde_get_context`**: Returns messages before and after a specific message index in a session, useful for expanding context around a search result.
- **Search result cache persistence**: MCP search results are cached both in memory and at `$XDG_CACHE_HOME/clotilde/search-results/<id>.json`, so `result_id` values survive MCP server restarts.
- **`clotilde_getting_started` prompt**: Auto-registered system prompt describing all available MCP tools and a recommended workflow.
- **Auto-register MCP in setup**: `clotilde setup` now registers the MCP server in `~/.claude.json` so it is available in all Claude Code sessions.

**Export**

- **HTML export search box**: The generated HTML includes a search box that filters messages in real time by text content.
- **Tool call and thinking block toggles**: "Show/Hide Tool Calls" and "Conversation Only" buttons in the HTML export header hide tool call divs and thinking blocks for cleaner reading.
- **Shared transcript parsing pipeline**: `internal/transcript` package provides a structured `Message` type and `Parse()` function used by both HTML export and the plain-text conversation viewer, avoiding duplicated client-side parsing logic.

**Daemon infrastructure**

- **Per-session model isolation via daemon**: A background gRPC daemon writes per-session `settings.json` files so `/model` changes in one Claude session do not bleed into others.
- **Flock-based daemon launch**: Concurrent `clotilde` invocations use `flock(2)` to serialise daemon startup, eliminating the race where two processes could both spawn the daemon.
- **Daemon monitor goroutine**: The daemon watches its own binary mtime and exits when a new build is installed, so `make install` takes effect immediately for the next session without a manual restart.
- **macOS LaunchAgent**: The daemon is registered as a launchd agent by default so it pre-warms at login. `clotilde setup --launch-agent` and `make install-launch-agent` manage registration.
- **Idle auto-exit**: The daemon shuts down gracefully after 5 minutes with no active sessions.
- **Code signing and notarization**: `make sign` and `make notarize` targets for Developer ID signing and Apple notarization, configured via `config.mk`.
- **`make install-launch-agent`**: Installs and bootstraps the LaunchAgent plist from the Makefile.

### Changed

- **Main `clotilde` command now uses tview app**: Replaced BubbleTea dashboard with native tview TUI application. All session management is now handled through the interactive session table, details pane, and overlay modals.
- **Arrow keys move highlight without opening details**: Arrow keys navigate the session table without opening the details pane. Spacebar explicitly opens the details pane with focus. Esc closes the details pane or exits (layered escape handling).
- **Table columns auto-size proportionally**: Session table columns now auto-size to available terminal width instead of using hardcoded widths, ensuring optimal use of screen space.
- **Session listing defaults to global**: `clotilde list`, dashboard, and resume picker now show all sessions globally instead of workspace-filtered. Pass `--workspace` / `-w` flag to filter to current workspace.
- **MCP server list/search/get tools**: Use `store.Resolve()` for unified session lookup instead of exact name matching.
- **Dashboard menu simplification**: Removed redundant "List all sessions" action, renamed "Resume session" to "Browse sessions".
- **Embedding model configurable**: Changed from hardcoded nomic-embed-text to configurable via `embedding_model` field in `search.local` config.
- **bench-embed model unloading**: Auto-unloads each model after benchmark completes to prevent LM Studio memory buildup.
- **`clotilde list` is workspace-scoped by default**: Shows only sessions whose `workspaceRoot` matches the current directory's project root. Pass `--all` to list every session regardless of workspace.
- **Dashboard "Search conversation"**: Now opens the unified search form TUI (session + query + depth) instead of a sequential picker-then-input flow.
- **Daemon installed as launchd agent by default**: The daemon previously required `--launch-agent` to register; it now registers at login automatically via `clotilde setup`.

### Removed

- **DisplayName field from session metadata**: Replaced by `store.Rename()` method for canonical naming. Sessions no longer store separate `displayName` fields.
- **GetByDisplayName store method**: `store.Resolve()` now handles all lookup strategies including display name matching.
- **Workspace-scoped session filtering as default**: Sessions now list globally by default; use `--workspace` flag to filter by workspace.

### Fixed

- **Scrollback artifacts on quit**: Fixed terminal scrollback corruption on exit by using explicit tcell screen initialization and cleanup.
- **Focus chain issues**: Fixed input capture issues by ensuring input capture is properly registered on correct primitives per tview architecture.
- **Transcript parser dropping 31.6% of user messages**: Fixed handling of array-format user content blocks that were previously invisible to parsing logic.
- **MCP search resolving wrong session**: Fixed edge case where multiple sessions with similar names would resolve to incorrect session due to exact name matching.
- **bench-embed memory accumulation**: Fixed multiple model instances loading without unloading, causing memory buildup.
- **Embedding model config not being read**: Fixed hardcoded nomic-embed-text backend; now respects `search.local.embedding_model` configuration.
- **Post-session dashboard layout**: Fixed alignment and rendering artefacts in the "Return to session" dashboard shown after a session exits.
- **Daemon binary not updated until next login**: `make install` now signals the running daemon to exit so the new binary is picked up immediately.

## [0.12.0] - 2026-04-08

### Added

- **`--model` on fork**: The `fork` command now accepts `--model` to override the inherited model, matching `start`, `incognito`, and `resume`.
- **`--model opus` defaults to 1M context**: `opus` is automatically normalized to `opus[1m]` so users get the 1M context window without needing to quote the suffix. Applies to `start`, `resume`, `fork`, and `incognito`, as well as profiles.

### Fixed

- **`--model opus` used 200K context instead of 1M**: Plain `opus` resolved to the 200K context variant. Now automatically maps to `opus[1m]`.

## [0.11.0] - 2026-03-31

### Added

- **`effortLevel` persisted in `settings.json`**: The `--effort` flag (and `--fast`, which implies `effort: low`) is now stored in the session's `settings.json` as `effortLevel`. This means the effort level is automatically applied on resume without needing to pass `--effort` again.
- **Session names in Claude's native picker**: All commands (`start`, `resume`, `fork`) now pass `-n <name>` to Claude, so clotilde session names appear as display names in Claude's built-in `/resume` picker and terminal title instead of raw UUIDs.
- **Fork inherits model and effort from parent**: Forked sessions now copy the parent's `settings.json` (including model and effort level). You can override with `--fast`, `--model`, or `--effort` on the fork command.

### Removed

- **Interactive Codebase Tours**: Removed `clotilde tour` command (list, serve, generate), `internal/tour/`, `internal/server/`, and streaming invocation code. Tours may return as a standalone project in the future.
- **Session statistics**: Removed `clotilde stats`, `stats backfill`, SessionEnd hook, crash recovery, and `--stats`/`--no-stats` flags from `setup`. Stats may return as a standalone tool in the future.
- **System prompt support**: Removed `--append-system-prompt`, `--append-system-prompt-file`, `--replace-system-prompt`, `--replace-system-prompt-file` flags, `systemPromptMode` metadata, `system-prompt.md` file handling, and all related store/invocation plumbing.

### Fixed

- **Lazy creation picks wrong project root**: `FindOrCreateClotildeRoot` would find a legacy `~/.claude/clotilde` (or any ancestor's) before checking the current project, causing new sessions to be created in the wrong directory. Now resolves the project root first (respecting the `$HOME` boundary) and only looks for `.claude/clotilde` within it. Also returns a clear error when the path exists but is not a directory, or when `os.Stat` fails for reasons other than "not found".
- **`clotilde fork` UUID mismatch**: Forked sessions ended up with the parent's UUID because `SessionStart` fires with the parent's UUID when using `--fork-session`. Fixed by pre-assigning the fork's UUID via `--session-id` before invocation, the same pattern used by `start`.
- **Propagate errors in fork custom output style handling**: `json.MarshalIndent`, `os.WriteFile`, and `store.Update` errors during fork style inheritance were previously silently discarded. They now fail loudly instead of leaving the fork in an inconsistent state.

## [0.10.0] - 2026-03-24

### Added

- **`--effort` flag**: Pass reasoning effort level (`low`, `medium`, `high`, `max`) through to Claude CLI on `start`, `resume`, `fork`, and `incognito` commands. Conflicts with `--fast` (which sets effort to `low` automatically). Includes shell completion for valid values.
- **`--model` on resume**: Override the model when resuming a session (e.g. `clotilde resume my-session --model opus`). Previously `--model` was only available on `start` and `incognito`.

### Fixed

- **Session leakage into `$HOME`**: `ProjectRootFromPath` now stops walking up at `$HOME`, preventing `~/.claude/` (Claude Code's global config) from being treated as a project marker. Previously, projects without their own `.claude/` directory would create sessions under `~/.claude/clotilde/sessions/`.

## [0.9.0] - 2026-03-23

### Added

- **Interactive Codebase Tours (experimental)**: New `clotilde tour` subcommand for browser-based interactive codebase walkthroughs with integrated Claude chat.
  - `clotilde tour list` — List available tours in `.tours/` directory
  - `clotilde tour serve [--port] [--model]` — Start interactive tour web server with code viewer, tour navigation, and chat sidebar
  - `clotilde tour generate [--focus] [--model]` — Generate tours automatically by analyzing the codebase with Claude
- **Tour features**:
  - **Persistent chat sessions** — Tours create a persistent `tour-<repo-name>` Clotilde session, preserving chat history across browser refreshes
  - **System prompt replacement** — Tour guide role fully replaces Claude's default system prompt (not appended) for focused, tour-specific context
  - **URL persistence** — Current step is saved to URL query parameter (`?step=N`) for bookmarking and resumability
  - **Chat reset** — Button to clear chat history and start a fresh conversation
  - **Tour format** — Supports CodeTour JSON format (`.tours/<name>.tour`)

## [0.8.1] - 2026-03-17

### Fixed

- **`--` passthrough with positional args**: Commands that support `-- <claude-flags>` (`start`, `resume`, `incognito`, `fork`) now correctly accept extra flags after `--`. Previously, Cobra's built-in arg validators counted args after `--` as positional args, causing an "accepts at most 1 arg(s), received 2" error (e.g. `clotilde start my-session -- LFG`).

## [0.8.0] - 2026-03-16

### Added

- **Git branch auto-naming**: Auto-naming commands (`start`, `incognito`, `fork`, and dashboard quick-actions) now use the current git branch name as the session name when not on `main` or `master` (e.g. branch `feature/gh-456` → session `feature-gh-456`). If the branch name is already taken, a numeric suffix is appended (`-2` through `-9`). Falls back to the existing `YYYY-MM-DD-adjective-noun` format on trunk branches, detached HEAD, or outside a git repo.
- **Full transcript history in `stats` and `export`**: Both commands now include all previous transcripts (from `/clear` operations tracked in `previousSessionIds`). `stats` sums turns across the full history and shows the earliest start time and latest activity. `export` produces HTML covering the complete conversation from the first message.
- **SessionEnd stats recording**: Opt-in SessionEnd hook that records session statistics (turns, tokens, models, tool usage) to daily JSONL files at `$XDG_DATA_HOME/clotilde/stats/`. Enable with `clotilde setup --stats`, disable with `--no-stats`. Includes crash recovery for sessions that exit without triggering SessionEnd.
- **`stats --all` flag**: Show aggregate stats across sessions active in the last 7 days (scoped to current project). Reads from JSONL stats files when available, falls back to transcript parsing.
- **`stats backfill` subcommand**: Generate JSONL stats records from existing session transcripts. Useful for populating stats after enabling tracking on an existing project.
- **Rich `stats` output**: Per-session stats now include token counts (input, output, cache read, cache write), models used, and tool usage breakdown (sorted by count, internal orchestration tools filtered out).

### Changed

- **`clotilde ls` model column**: Reads only the last 128KB of each transcript file instead of the full file, significantly reducing load time for projects with many or large sessions.
- **`clotilde ls` last-used column**: Now reads the timestamp of the last entry in the transcript instead of the file mtime, giving a more accurate and meaningful activity time. Also updated on every hook-driven session start, not just on explicit `clotilde resume`.

### Fixed

- **Third-party hook preservation**: `clotilde setup` now correctly preserves non-clotilde hooks (e.g. zellaude) when merging hook configuration, instead of stripping them.

## [0.7.0] - 2026-03-11

### Added

- **`setup` command**: `clotilde setup` registers SessionStart hooks in `~/.claude/settings.json` (global). Run once after installing. Supports `--local` flag for `~/.claude/settings.local.json`. Idempotent and merges with existing settings.
- **Lazy session directory creation**: `clotilde start` (and other session-creating commands) automatically create `.claude/clotilde/sessions/` on first use. No `init` required.
- **Double-hook execution guard**: Prevents duplicate context output when both global and per-project hooks exist (migration safety).
- **`export` command**: `clotilde export <name>` renders a session transcript into a self-contained HTML file. Dark theme, markdown rendering, syntax highlighting, per-tool formatting, collapsible thinking blocks, expandable tool outputs, and keyboard shortcuts (Ctrl+T, Ctrl+O). Supports `-o` for custom output path and `--stdout` for piping.
- **`hook notify` subcommand**: Logs Claude Code hook events (Stop, Notification, PreToolUse, PostToolUse, SessionEnd) to `/tmp/clotilde/<session-id>.events.jsonl` for debugging. Opt-in only, not registered by default setup.

### Changed

- **Session-reading commands**: `list`, `resume`, `inspect`, `delete`, and `export` show friendly "no sessions found" messages instead of "not in a clotilde project" errors.
- **Dashboard**: Opens in any directory (auto-creates session storage). Empty session list is handled gracefully.

### Deprecated

- **`init` command**: Replaced by `setup`. Still works but prints a deprecation notice.

### Removed

- **`context.md` file**: The deprecated global context file (`.claude/clotilde/context.md`) has been removed. Use the `--context` flag instead.
- **Auto-created `config.json`**: Project-level config is no longer created automatically. Profiles still work if the file exists.

### Fixed

- **Dashboard start action**: Now auto-generates a session name and launches Claude directly instead of printing a placeholder message.
- **Dashboard fork action**: Now shows a session picker, auto-generates a fork name, and launches Claude instead of printing a placeholder message.

## [0.6.0] - 2026-03-08

### Changed

- **Auto-generated session names**: `start` no longer requires a name argument. When omitted, a date-prefixed name is generated automatically (e.g. `2026-03-08-happy-fox`). The `incognito` command uses the same `YYYY-MM-DD-adjective-noun` format.

## [0.5.0] - 2026-02-23

### Added

- **Global profiles**: Profiles can now be defined in `~/.config/clotilde/config.json` (respects `$XDG_CONFIG_HOME`). Global profiles are available in all projects. Project-level profiles take precedence over global ones when names collide. CLI flags still override both.
- **`--context` flag**: Attach context to sessions (e.g. `--context "working on ticket GH-123"`). Available on `start`, `incognito`, `fork`, and `resume` commands. Context is stored in session metadata and automatically injected into Claude at session start alongside the session name. Forked sessions inherit context from the parent unless overridden.
- **Session name injection**: The session name is now automatically output to Claude at session start via the SessionStart hook.

### Deprecated

- **`context.md` file**: Global context file (`.claude/clotilde/context.md`) is deprecated in favor of the `--context` flag. It will be removed in 1.0.

## [0.4.0] - 2026-02-20

### Added

- **Session profiles**: New named presets in `.claude/clotilde/config.json` for grouping model, permissions, and output style settings. Use `--profile <name>` when creating sessions. CLI flags override profile values.
  - Example: `clotilde start my-session --profile quick` applies the "quick" profile, then allows CLI flags to override individual settings
  - Profiles can contain: `model`, `permissionMode`, `permissions` (allow/deny/ask/additionalDirectories/defaultMode), and `outputStyle`

### Removed

- **Implicit global defaults**: Removed `model` and `permissions` from top-level config. Use profiles instead for explicit, named presets.

### Changed

- **Config structure**: `Config` now has `profiles` (map of Profile) instead of `DefaultModel`/`DefaultPermissions` fields

## [0.3.1] - 2026-02-18

### Fixed

- **Empty session detection with symlinks:** Sessions were incorrectly detected as empty (and auto-removed) when the project path involved symlinks. The transcript path saved by the SessionStart hook is now used for detection instead of recomputing it from the clotilde root, which could resolve to a different path than what Claude Code uses.

## [0.3.0] - 2026-02-17

### Added

- **Permission mode shortcuts**: `--accept-edits`, `--yolo`, `--plan`, `--dont-ask` as shorthand for `--permission-mode <value>` on `start`, `incognito`, `resume`, and `fork` commands
- **`--fast` composite preset**: Sets `--model haiku` and `--effort low` in a single flag for quick, low-cost sessions
- Conflict detection for mutually exclusive shorthand flags (e.g., `--accept-edits` + `--yolo`, or `--fast` + `--model`)

### Fixed

- **Ghost session cleanup:** Sessions created with `start` or `fork` are automatically removed if the user exits Claude Code without sending any messages (no transcript created)

### Changed

- **`start` command**: Instead of failing when a session with the same name exists, now prompts the user to resume it (in TTY mode) or suggests `clotilde resume <name>` (in non-TTY mode)
- **`resume` command refactored** from global variable to factory function (`newResumeCmd()`), enabling flag registration and consistent test isolation

## [0.2.0] - 2025-12-04

### Changed

- **Context system simplified:** Removed session-specific context support. Now only supports global context (`.claude/clotilde/context.md`)
- **Context source header:** Global context now includes a header indicating its source file, making it easier for Claude to know where to update context
- **Fork behavior:** Forks no longer inherit context from parent sessions (only settings and system prompt)
- **Documentation:** Updated docs to be worktree-agnostic (context works with or without git worktrees)

### Removed

- `LoadContext()` and `SaveContext()` methods from session store
- Session-specific `context.md` files (no longer created or copied during fork)

### Fixed

- Goreleaser archive configuration: Split into separate unix (tar.gz) and windows (zip) configurations for clearer build output

## [0.1.0] - 2025-12-02

Initial release of Clotilde - named session management for Claude Code.

### Added

**Core Features:**
- Named sessions with human-friendly identifiers (vs UUIDs)
- Session forking to explore different approaches
- Incognito sessions (auto-delete on exit) 👻
- Custom model and system prompt support per session
- System prompt replacement (replace Claude's default entirely)
- Two-level context system (global + session-specific)
- Persistent permission settings per session
- Pass-through flags support (forward arbitrary Claude Code flags)
- Full session cleanup (removes session data and Claude Code transcripts)
- Shell completion for bash, zsh, fish, and PowerShell

**Commands:**
- `init` - Initialize clotilde in a project
- `start` - Start new named sessions
- `incognito` - Start incognito sessions (auto-delete on exit)
- `resume` - Resume existing sessions
- `list` - List all sessions (table format)
- `inspect` - Show detailed session information with excerpts
- `fork` - Fork sessions (including incognito forks)
- `delete` - Delete sessions and associated data

**Enhancements:**
- Command aliases for common operations
- Table-formatted session list
- Inspect shows 200-char excerpts for prompts and context
- Humanized file sizes in inspect output
- System prompt content display in inspect
- Hide empty Settings section when no settings configured
- Support for `/compact` and `/clear` operations via unified SessionStart hook

**Documentation:**
- README with installation and usage guide
- CONTRIBUTING.md for contributors
- GitHub issue/PR templates
