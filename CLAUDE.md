# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Purpose

Clyde is a Go wrapper around Claude Code with a four-verb CLI surface,
a TUI dashboard, and a long-lived background daemon. It exists for
named-session resume, append-only compaction, an OpenAI-compatible
HTTP shim that fronts Claude, and an MCP server for in-chat session
search. Anything not in that list went away in the wipe-to-core cull.

Surface (post-cull):

```
clyde                       -> TUI dashboard (manage existing sessions)
clyde compact ...           -> append-only compaction
clyde daemon                -> background daemon (adapter + oauth + mcp + prune)
clyde hook sessionstart     -> Claude Code SessionStart hook
clyde mcp                   -> MCP stdio server (search/list/context)
clyde resume <name|uuid>    -> resolve clyde name -> claude --resume <uuid>
clyde -r / --resume <x>     -> rewritten by dispatch to `clyde resume <x>`
anything else               -> ForwardToClaude (transparent passthrough)
```

The TUI is read-mostly with management actions wired via direct Go
calls into the daemon: resume, delete, rename, view content, send-to,
tail-transcript, remote-control toggle, bridge listing. Session
creation and incognito went away with the cull; users create sessions
via plain `claude` (passthrough) and clyde adopts them in the
background.

## Architecture

### Core Concept

Clyde is a **thin, non-invasive wrapper**. It:

- Generates UUIDs and stores name→UUID mappings in `.claude/clyde/sessions/<name>/metadata.json`
- Invokes `claude` CLI with the mapped UUID
- Never modifies Claude Code itself

### Session Structure

Each session is a folder in `.claude/clyde/sessions/<name>/`:

```
.claude/clyde/
  config.json             # Project config (profiles - optional, created manually)
  sessions/
    my-session/
      metadata.json       # Session metadata (name, sessionId, timestamps, parent info)
      settings.json       # Claude Code settings (model, permissions - optional)
```

**Metadata format** (`metadata.json`):

```json
{
  "name": "my-feature",
  "sessionId": "uuid-for-claude-code",
  "transcriptPath": "/home/user/.claude/projects/.../uuid.jsonl",
  "created": "2025-11-23T10:30:00Z",
  "lastAccessed": "2025-11-23T18:42:00Z",
  "parentSession": "original-session",
  "isForkedSession": true,
  "isIncognito": false,
  "previousSessionIds": ["old-uuid-1", "old-uuid-2"],
  "context": "working on ticket GH-123"
}
```

`**previousSessionIds**`: Array of UUIDs from `/clear` operations. When Claude Code clears a session, it creates a new UUID. Clyde tracks the old UUIDs here for complete cleanup on deletion. Note: `/compact` does NOT currently create a new UUID (only `/clear` does), but we handle it defensively in the code.

`**isIncognito**`: Boolean flag. If true, session auto-deletes on exit (via defer-based cleanup in `invoke.go`). Incognito sessions are useful for quick queries, experiments, or sensitive work. Cleanup runs on normal exit and Ctrl+C, but not on SIGKILL or crashes.

`**context**`: Optional free-text field set via `--context` flag on `start`, `incognito`, `fork`, and `resume` commands. Injected into Claude via the SessionStart hook alongside the session name. Forked sessions inherit context from the parent unless overridden. Context can be updated on resume (e.g. `clyde resume my-session --context "now on GH-456"`).

**Project config format** (`.claude/clyde/config.json`):

```json
{
  "profiles": {
    "quick": {
      "model": "haiku",
      "permissionMode": "bypassPermissions"
    },
    "strict": {
      "permissions": {
        "deny": ["Bash", "Write"],
        "defaultMode": "ask"
      }
    },
    "research": {
      "model": "sonnet",
      "outputStyle": "Explanatory"
    }
  }
}
```

**Global config format** (`~/.config/clyde/config.json`):

Same structure as the project config. Respects `$XDG_CONFIG_HOME` if set, otherwise defaults to `~/.config/clyde/config.json`. Profiles defined here are available in all projects.

**Config purpose**: Define named session presets (profiles) for common configurations. Use `clyde start <name> --profile <profile>` to apply a profile.

**Profile fields**:

- `model` - Claude model (haiku, sonnet, opus)
- `permissionMode` - Permission mode (acceptEdits, bypassPermissions, default, dontAsk, plan)
- `permissions` - Granular permissions: allow/deny/ask lists, additionalDirectories, defaultMode, disableBypassPermissionsMode
- `outputStyle` - Output style (built-in or custom name)

**Precedence**: Global profile → project profile → CLI flags (each layer overrides the previous). For example, if both global and project configs define a `"quick"` profile, the project version wins. CLI flags always override profile values.

**Settings format** (`settings.json`):

```json
{
  "model": "sonnet",
  "permissions": {
    "allow": ["Bash", "Read"],
    "deny": ["Write"],
    "ask": [],
    "additionalDirectories": [],
    "defaultMode": "ask",
    "disableBypassPermissionsMode": "false"
  }
}
```

**Settings scope**: Only session-specific settings (model, permissions). Not global stuff like hooks, MCP servers, status line. Settings file is ALWAYS created (empty object if no model/permissions specified).

**Context loading**: Context is injected at session start via SessionStart hooks:

- **Session name**: Always output if available
- **Session context**: From metadata `context` field (set via `--context` flag)

### Claude Code Integration Patterns

**Starting a session:**

```bash
claude --session-id <uuid> \
  --settings .claude/clyde/sessions/<name>/settings.json
```

**Resuming a session:**

```bash
claude --resume <uuid> \
  --settings .claude/clyde/sessions/<name>/settings.json
```

**Forking a session:**

```bash
claude --resume <parent-uuid> --fork-session \
  --session-id <fork-uuid> -n <fork-name> \
  [--settings ...]
```

Note: `--settings` is only added if the file exists. `--session-id` pre-assigns the fork's UUID (avoids hook-based UUID registration). `-n` sets the display name shown in Claude's native session picker.

### Session Hooks

**Unified SessionStart hook** (`clyde hook sessionstart`) handles all session lifecycle events internally based on the `source` field in JSON input:

**Hook registration** (in `~/.claude/settings.json`, installed by `make install-hook`; `clyde setup` was removed in the cull):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [{ "type": "command", "command": "clyde hook sessionstart" }]
      }
    ]
  }
}
```

No matcher field - the single hook handles all sources (startup, resume, compact, clear) internally.

**Source-based dispatch:**

- `**startup`**: New sessions - outputs session name and context, saves transcript path
- `**resume`**: Resuming or `clyde fork` - outputs context
- `**compact**`: Session compaction - defensive handler (Claude Code doesn't currently create new UUID for `/compact`, but we handle it anyway in case behavior changes)
- `**clear**`: Session clear - updates metadata with new UUID, preserves old UUID in `previousSessionIds` array

`**clyde fork` registration:**

1. `clyde fork` pre-assigns a UUID via `util.GenerateUUID()` before creating the session
2. Sets env var: `CLYDE_SESSION_NAME` (for context output in hook)
3. Invokes `claude --resume <parent> --fork-session --session-id <forkUUID> -n <forkName>`
4. Claude triggers SessionStart with `source: "resume"` → hook outputs context for the new session
5. Fork UUID is guaranteed to match because it was pre-assigned (no hook-based UUID registration needed)

**Clear handling:**

1. User runs `/clear` in Claude Code
2. Claude creates new UUID and triggers SessionStart with `source: "clear"`
3. Hook resolves session name using three-level fallback:
  - Priority 1: `CLYDE_SESSION_NAME` env var (from `clyde resume`)
  - Priority 2: Read from `CLAUDE_ENV_FILE` (persisted by previous hook)
  - Priority 3: Reverse UUID lookup in sessions (searches current and previous IDs)
4. Hook calls `session.AddPreviousSessionID()` to update metadata:
  - Appends current UUID to `previousSessionIds` array (idempotent)
  - Updates `sessionId` to new UUID
5. Session name persists across multiple `/clear` operations

**Note on `/compact`:** Currently, Claude Code does NOT create a new session UUID when `/compact` is run (only `/clear` does). However, the hook defensively handles `source: "compact"` identically to `source: "clear"` in case Claude Code's behavior changes in the future.

**Context loading:**

- Hook outputs context to stdout which gets automatically injected by Claude Code
- Session name is always output if available (e.g. "Session name: my-feature")
- Session context from metadata is output if set (e.g. "Context: working on GH-123")
- Hooks use os.Stdin piping to read JSON input from Claude Code

### Claude Code Path Conversion

Claude Code stores project data in `~/.claude/projects/` with paths like:

```
/home/user/project/foo.bar → ~/.claude/projects/-home-user-project-foo-bar/
```

Conversion rule: Replace `/` and `.` with `-`

Implementation in `internal/claude/paths.go`:

- `ProjectDir(clydeRoot)` - Converts `.claude/clyde` parent to Claude's project dir format
- Used for deleting transcripts/agent logs

### Delete Behavior

When deleting a session, remove:

- Session folder: `.claude/clyde/sessions/<name>/`
- Claude transcript (current): `~/.claude/projects/<project-dir>/<uuid>.jsonl`
- Claude transcripts (previous): For each UUID in `previousSessionIds` array
- Agent logs: `~/.claude/projects/<project-dir>/agent-*.jsonl` (grep for all sessionIds)

This ensures complete cleanup even after multiple `/clear` operations (and `/compact`, if Claude Code's behavior changes to create new UUIDs for compaction).

### OpenAI compatible adapter

The daemon optionally hosts an OpenAI Chat Completions v1 HTTP surface (`internal/adapter/`). One launchd entry boots both the gRPC daemon and this adapter. The adapter resolves a request's `model` + `reasoning_effort` through a built in registry (`claude-4-7-{low,med,high,max-thinking}` at 1M, `sonnet`/`haiku`/`claude-opus` at 200k, `gpt-4o` shunt) plus user overrides in `adapter.models` and upstream forwards in `adapter.shunts`. Version one supports streaming, non streaming, bearer auth, and a concurrency semaphore. Tool calling, images, and embeddings are rejected with a 400. See `docs/openai-adapter.md`.

### Remote Control (`--remote-control`)

Sessions can opt into Claude Code's bridge so the running conversation is exposed at `https://claude.ai/code/<bridgeSessionId>`. Three layers cooperate:

1. **Wrapper** (`internal/claude/pty_invoke.go`): when `RemoteControl` is on, claude runs inside a pty using `github.com/creack/pty`. The wrapper opens a per-session Unix socket at `$XDG_RUNTIME_DIR/clyde/inject/<sessionId>.sock` (or `$TMPDIR/clyde-inject/...` on macOS) and copies inbound bytes into the pty stdin, so daemon-mediated messages reach claude as if typed.
2. **Daemon** (`internal/bridge/`, `internal/daemon/`): one `bridge.Watcher` per daemon process tails `~/.claude/sessions/<pid>.json` via fsnotify and emits `BRIDGE_OPENED` / `BRIDGE_CLOSED` on the existing registry stream. `transcript.Tailer` plus `transcriptHub` fan transcript lines out to multiple subscribers via the new `TailTranscript` server-side streaming RPC. `SendToSession` dials the wrapper's inject socket. `UpdateSessionSettings` and `UpdateGlobalSettings` are the daemon-authoritative write paths for per session / global config.
3. **TUI** (`internal/ui/`): The dashboard shows an `RC` badge column, a "Remote ctrl" details row, an `RC×N` status bar badge, and "Open bridge in browser" / "Copy bridge URL" entries in the options popup. Press `S` to pin a session in the new "Sidecar" tab (`internal/ui/tcell_sidecar.go`), which subscribes to `TailTranscript` and posts user input through `SendToSession`. Press `G` on the Settings tab to flip the global default.

Post-cull, the bridge is reachable through the TUI only (RC toggle in  
the options popup, Sidecar tab for tail/send). The standalone  
`clyde bridge` and `clyde send` verbs were removed; their daemon RPCs  
(`UpdateSessionSettings`, `TailTranscript`, `SendToSession`,  
`ListBridges`) still exist and are driven directly by the TUI.

## Testing

**Test Organization:**

- 7 Ginkgo test suites: `cmd/`, `internal/claude/`, `internal/config/`, `internal/export/`, `internal/notify/`, `internal/session/`, `internal/util/`
- Unit tests for core functionality
- Integration tests using fake claude binary (internal/testutil)
- os.Pipe() for testing hook stdin/stdout communication
- Isolated test environments with temp directories

**Testing Philosophy:**

- **Write tests alongside code changes** - new features and bug fixes should include test coverage
- Test both success and error cases
- Keep tests focused and independent
- Use descriptive test names

## Documentation

### Core Concepts

- **[Claude Settings Behavior](docs/claude-settings-behavior.md)** - Detailed analysis of how Claude Code's `--settings` flag, permission system, and multi-layer settings work. Critical for understanding Clyde's design decisions around session isolation and permission handling.

## Key Constraints

- **Minimal wrapper**: Don't reinvent Claude Code features, just wrap them
- **Non-invasive**: Never patch or modify Claude Code binaries
- **Stable format**: Session structure should remain consistent across versions
- **Single binary**: No runtime dependencies (Go only)
- **Settings scope**: `settings.json` should only contain session-specific settings (model, permissions), not global config (hooks, MCP, UI)
- **Native integration**: Use `--settings` flag to pass settings, let Claude Code handle merging with global/project configs

# Clyde unified slog standard

Every operation in the clyde codebase MUST emit at least one
structured `slog` event. No exceptions. This includes ticks, clicks,
switches, plumb operations, hook fires, file reads, network calls,
state mutations, and decisions. The unified JSONL trace at
`$XDG_STATE_HOME/clyde/clyde.jsonl` is the only way we can
debug across the daemon + adapter + TUI + hooks + MCP.

## The setup

`internal/slogger` wraps `goodkind.io/gklog` (the cross-repo logging
package) and the request-scoped context.WithLogger pattern from
`tack/internal/telemetry`.

At process start (daemon main, CLI root command, hook entrypoints):

```go≈
import "goodkind.io/clyde/internal/slogger"

closer, err := slogger.Setup()
if err != nil {
    // Init failed; gklog returned an error. log via slog.Default
    // (stderr text fallback) and exit.
    slog.Error("slogger setup failed", "err", err)
    os.Exit(1)
}
defer closer.Close()
```

`slogger.Setup` calls `gklog.New` with the JSONL file path, which:

- Writes JSON to `$XDG_STATE_HOME/clyde/clyde.jsonl` with
rotation (5 MB / forever / compressed by default).
- Also writes JSON to stdout so journald / launchd captures it.
- Annotates every record with `build` from `goodkind.io/gklog/version`.
- Calls `slog.SetDefault` so the rest of the codebase just uses `slog`.

## Emitting events

Use Go's standard `log/slog` directly. No wrapper, no helper:

```go
slog.Info("adapter.chat.completed",
    "request_id", reqID,
    "session", sessionName,
    "model", model.Alias,
    "tokens_in", usage.PromptTokens,
    "tokens_out", usage.CompletionTokens,
    "duration_ms", time.Since(started).Milliseconds(),
)
```

For request-scoped fields, attach them to a logger and stash in ctx:

```go
import "goodkind.io/clyde/internal/slogger"

func handleChat(w http.ResponseWriter, r *http.Request) {
    reqID := newRequestID()
    log := slog.Default().With(
        "request_id", reqID,
        "component", "adapter",
    )
    ctx := slogger.WithLogger(r.Context(), log)
    // ...downstream code does:
    //   slogger.L(ctx).Info("step.parsed", "ms", n)
}
```

## Required field conventions

`gklog` automatically attaches `build`. The caller MUST supply the
event message as the first argument (the slog convention) and SHOULD
include the keys below whenever they apply, so cross-component
queries join cleanly:


| Key           | Meaning                                               |
| ------------- | ----------------------------------------------------- |
| `component`   | Subsystem owning the call (`adapter`, `compact`, ...) |
| `request_id`  | Per-incoming-request correlation id                   |
| `session`     | Clyde session name                                    |
| `session_id`  | Claude session UUID                                   |
| `transcript`  | Absolute path to the JSONL transcript on disk         |
| `model`       | Resolved Claude model name                            |
| `alias`       | Public model alias (clyde-haiku, ...)                 |
| `tokens_in`   | Prompt tokens                                         |
| `tokens_out`  | Completion tokens                                     |
| `duration_ms` | Operation latency in milliseconds                     |
| `err`         | Error message string (only set on Error)              |


Levels:

- `slog.Debug(msg, ...)` for inner loops, ticks, polls.
- `slog.Info(msg, ...)` for state mutations and significant decisions.
- `slog.Warn(msg, ...)` for recovered or degraded paths.
- `slog.Error(msg, ...)` for failures.

Event names use dot-separated `component.subject.verb` form, lowercase
snake_case where multi-word: `adapter.chat.completed`,
`compact.boundary.lifted`, `verify.context.probe.parsed`.

## Banned patterns

`make slog-audit` rejects any production .go file containing:

- `fmt.Print`, `fmt.Println`, `fmt.Printf` (bare stdout writes).
- `log.Print*`, `log.Fatal*`, `log.Panic*` (stdlib log goes nowhere
structured).

Allowed (these go through writers the test harness can capture):

- `fmt.Fprint*` to a writer (`cmd.OutOrStdout()`, `os.Stderr` in
bootstrap-only paths).
- `slog.Info / Debug / Warn / Error` directly. The wrapper at
`internal/slogger` only handles initialization and ctx plumbing;
there is no banned slog method.

Exempt files (audit walks past them):

- `_test.go` files -- tests can use any logging shape.
- `scripts/`, `research/`, `vendor/`, `node_modules/`.
- `cmd/version.go`, `cmd/completion.go` -- bootstrap output before
the slog system is initialized.

## Audit tool

`make slog-audit` greps the tree, prints per-package counts and the
first 30 offending call sites, exits non-zero on hits. CI runs this
on every PR. Local runs let contributors find their own violations
before pushing.

## Migration

Existing modules are converted in waves by parallel Haiku subagents.
New code is held to the standard from day one. The audit tool reports
a per-package count so contributors can see which area has the
largest backfill remaining.