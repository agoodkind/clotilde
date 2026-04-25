# AGENTS.md

This file guides coding agents (including Claude Code and similar tools) when working in this repository. It replaces the former `CLAUDE.md` and adds agent tooling notes below.

## Project Purpose

Clyde is a Go wrapper around Claude Code with a small first-party CLI
(`clyde` subcommands plus transparent passthrough to `claude`), a TUI
dashboard, and a long-lived background daemon. It exists for
named-session resume, append-only compaction, an OpenAI-compatible
HTTP shim that fronts Claude, and an MCP server for in-chat session
search. Anything not in that list went away in the wipe-to-core cull.

Surface (post-cull), matching `cmd/clyde/main.go`:

```
clyde                       -> TUI dashboard (manage existing sessions)
clyde compact ...           -> append-only compaction
clyde daemon                -> background daemon (adapter + oauth + mcp + prune)
clyde hook sessionstart     -> Claude Code SessionStart hook
clyde mcp                   -> MCP stdio server (search/list/context)
clyde resume <name|uuid>    -> resolve clyde name -> claude --resume <uuid>
clyde -r / --resume <x>     -> rewritten by dispatch to `clyde resume <x>`
clyde exec|api ...          -> ClassifyArgs passthrough -> claude (no post-TUI for api; same for -p/--print)
anything else               -> cobra; unknown -> ForwardToClaudeThenDashboard (TTY may open TUI after exit)
```

`compact`, `daemon`, and `mcp` are **clyde-owned** names; the real `claude` binary also defines `compact`, `daemon`, and `mcp` with different behavior, so users who need the stock subcommands should invoke `claude` directly. After forward, `cmd/root.go` skips the post-claude TUI for `api`, `-p`/`--print`, and a set of common one-shot first-arg subcommands (aligned with Claude Code `cli.tsx` / `main.tsx`).

Developer tooling: **`cmd/clyde-tui-qa`** drives the real TUI for QA (see the section near the end of this file). It is not part of the default user surface.

The TUI is read-mostly with management actions wired via direct Go
calls into the daemon: resume, delete, rename, view content, send-to,
tail-transcript, remote-control toggle, bridge listing. Session
creation and incognito went away with the cull; users create sessions
via plain `claude` (passthrough) and clyde adopts them in the
background.

### TUI-as-a-Dumb-Renderer

Treat the TUI as a dumb renderer over daemon-owned and domain-owned state.

- Put business logic, normalization, filtering, aggregation, and transcript shaping upstream in shared packages or daemon RPC construction.
- Do not add TUI-only semantic cleanup, transcript parsing, cache aggregation, provider accounting, or state derivation when the same logic can live in `internal/transcript`, `internal/claude`, `internal/adapter`, or daemon/server code.
- The TUI may own presentation concerns only:
  - layout
  - focus
  - scrolling
  - wrapping
  - truncation
  - visual grouping
  - badges and status text
- The TUI should consume already-shaped data models. If a screen needs “cleaner” or “smarter” data, fix the upstream producer and keep the renderer simple.
- Prefer one canonical pipeline for conversation/plain-text views and reuse it everywhere: daemon details, MCP/session export, search snippets, and TUI transcript panes.

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

`**context**`: Optional free-text in metadata. The SessionStart hook prints it when set (see `internal/hook/handlers.go`). The daemon can refresh it via `UpdateContext` from the live TUI (`cmd/session_helpers.go`). There is no `clyde` flag today that sets this field. Edit metadata or rely on those code paths. Forks created with Claude Code `claude --resume ... --fork-session` use whatever context the store holds for the adopted row.

**Project config file** (`.claude/clyde/config.json`, the path the Settings tab and `E` open in the TUI):

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

**Global config** (`internal/config/load.go`, `LoadGlobalOrDefault`): read from `$XDG_CONFIG_HOME/clyde/` (default `~/.config/clyde/`). **`config.toml` is preferred; `config.json` is used if TOML is absent.** `SaveGlobal` writes TOML only.

The `Config` struct in `internal/config/config.go` includes `defaults`, `profiles`, `logging`, `adapter`, `search`, and other sections. **`profiles` exists in the on-disk schema. No production code path reads `cfg.Profiles` outside config tests today**, so do not document a `clyde` CLI that applies a profile by name until that wiring lands.

**Example profile-shaped fields** (for reference when authoring JSON or TOML by hand):

- `model`: Claude model (haiku, sonnet, opus)
- `permissionMode`: acceptEdits, bypassPermissions, default, dontAsk, plan
- `permissions`: allow/deny/ask lists, additionalDirectories, defaultMode, disableBypassPermissionsMode
- `outputStyle`: built-in or custom name

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
- **Session context**: From metadata `context` when non-empty

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

- **`startup`**: New sessions. Outputs session name and context, saves transcript path.
- **`resume`**: Resuming or fork flows. Outputs context when metadata has it.
- **`compact`**: Session compaction. Defensive handler (Claude Code does not currently create a new UUID for `/compact`, but we handle it anyway in case behavior changes).
- **`clear`**: Session clear. Updates metadata with new UUID and preserves old UUID in `previousSessionIds`.

**Forking with Claude Code (no `clyde fork` verb):**

1. Use `claude --resume <parent-uuid> --fork-session` with a new `--session-id` and `-n` name as in the examples above.
2. The SessionStart hook sees `source: "resume"` for the new process and runs the same path as a normal resume.
3. `CLYDE_SESSION_NAME` and transcript registration still apply when the hook adopts or updates the fork row.

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

The daemon optionally hosts an OpenAI Chat Completions v1 HTTP surface under `internal/adapter/`. Incoming `model` strings resolve through a registry built from `[adapter]` and `[adapter.models]` in config (`internal/adapter/models.go`). Backends include direct Claude, Anthropic HTTP, configured shunts, and the local `claude` CLI fallback (`BackendFallback`). See `reasoning_effort` and family `efforts` in the adapter packages for how effort maps to wire format. Streaming and non streaming paths exist; tool calling, images, and embeddings policies are enforced in the dispatcher. There is no checked in `docs/openai-adapter.md`; read the code and config schema as the source of truth.

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

- Ginkgo specs under `internal/claude/`, `internal/cli/hook/`, `internal/config/`, `internal/notify/`, `internal/session/`, and `internal/util/` (files using `Describe` / `It`).
- Standard `testing` tests elsewhere (for example `internal/ui/*_test.go` with `tcell.SimulationScreen`, `internal/adapter/*_test.go`, `internal/tuiqa/keys_test.go`).
- Integration style coverage where tests fake or stub the `claude` subprocess.
- Hook tests use `os.Pipe()` for stdin and stdout where applicable.
- Isolated temp dirs for filesystem heavy cases.

**Testing Philosophy:**

- **Write tests alongside code changes** - new features and bug fixes should include test coverage
- Test both success and error cases
- Keep tests focused and independent
- Use descriptive test names

## Documentation

### Core Concepts

- **Claude Code settings**: There is no `docs/claude-settings-behavior.md` in this repo. For `--settings`, permissions, and merge order, use Anthropic or Claude Code product documentation. Clyde passes per session `settings.json` paths into `claude` where the invoke path builds the argv list (`internal/claude/invoke.go`).

## Key Constraints

- **Minimal wrapper**: Don't reinvent Claude Code features, just wrap them
- **Non-invasive**: Never patch or modify Claude Code binaries
- **Stable format**: Session structure should remain consistent across versions
- **Single binary**: No runtime dependencies (Go only)
- **Settings scope**: `settings.json` should only contain session-specific settings (model, permissions), not global config (hooks, MCP, UI)
- **Native integration**: Use `--settings` flag to pass settings, let Claude Code handle merging with global/project configs

## Clyde unified slog standard (P0)

Every operation in the clyde codebase MUST emit at least one
structured `slog` event. No exceptions. This includes ticks, clicks,
switches, plumb operations, hook fires, file reads, network calls,
state mutations, and decisions. The unified JSONL trace at
`$XDG_STATE_HOME/clyde/clyde.jsonl` is the only way we can
debug across the daemon + adapter + TUI + hooks + MCP.

### The setup

`internal/slogger` wraps `goodkind.io/gklog` for process setup (`Setup`
only). Request scoped loggers on `context.Context` use `goodkind.io/gklog`
(`WithLogger`, `LoggerFromContext`, and optional `L`).

At process start (daemon main, CLI root command, hook entrypoints):

```go
import "goodkind.io/clyde/internal/slogger"
import "goodkind.io/clyde/internal/config"

cfg := config.LoggingConfig{
    // loaded from config.toml
}
closer, err := slogger.Setup(cfg)
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
  configurable rotation from `[logging.rotation]`.
- Writes no JSON to stdout so CLI command output remains stable.
- Annotates every record with `build` from `goodkind.io/gklog/version`.
- Calls `slog.SetDefault` so the rest of the codebase just uses `slog`.

### Emitting events

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
import "goodkind.io/gklog"

func handleChat(w http.ResponseWriter, r *http.Request) {
    reqID := newRequestID()
    log := slog.Default().With(
        "request_id", reqID,
        "component", "adapter",
    )
    ctx := gklog.WithLogger(r.Context(), log)
    // ...downstream code does:
    //   gklog.LoggerFromContext(ctx).InfoContext(ctx, "step.parsed", "ms", n)
}
```

### Required field conventions

`gklog` automatically attaches `build`. The caller MUST supply the
event message as the first argument (the slog convention) and SHOULD
include the keys below whenever they apply, so cross-component
queries join cleanly:

| Key            | Meaning                                               |
| -------------- | ----------------------------------------------------- |
| `component`    | Subsystem owning the call (`adapter`, `compact`, ...) |
| `subcomponent` | Internal emitter inside a top-level component         |
| `request_id`   | Per-incoming-request correlation id                   |
| `session`      | Clyde session name                                    |
| `session_id`   | Claude session UUID                                   |
| `transcript`   | Absolute path to the JSONL transcript on disk         |
| `model`        | Resolved Claude model name                            |
| `alias`        | Public model alias (clyde-haiku, ...)                 |
| `tokens_in`    | Prompt tokens                                         |
| `tokens_out`   | Completion tokens                                     |
| `duration_ms`  | Operation latency in milliseconds                     |
| `err`          | Error message string (only set on Error)              |

Levels:

- `slog.Debug(msg, ...)` for inner loops, ticks, polls.
- `slog.Info(msg, ...)` for state mutations and significant decisions.
- `slog.Warn(msg, ...)` for recovered or degraded paths.
- `slog.Error(msg, ...)` for failures.

Event names use dot-separated `component.subject.verb` form, lowercase
snake_case where multi-word: `adapter.chat.completed`,
`compact.boundary.lifted`, `verify.context.probe.parsed`.

### adapter.chat.raw logging

`adapter.chat.raw` is controlled by `[logging.body]`:

| Mode        | Captured fields                     |
| ----------- | ----------------------------------- |
| `summary`   | `body_summary`                      |
| `whitelist` | `body_summary` and sanitized `body` |
| `raw`       | `body_summary` and full raw `body`  |
| `off`       | no `adapter.chat.raw` event         |

When `mode = "whitelist"`, the sanitized body keeps request metadata and
`messages`, trims each message content to 2 KiB, strips tool parameter
schemas, and caps the logged body at `[logging.body].max_kb`.

### Banned patterns

`make slog-audit` rejects any production .go file containing:

- `fmt.Print`, `fmt.Println`, `fmt.Printf` (bare stdout writes).
- `log.Print*`, `log.Fatal*`, `log.Panic*` (stdlib log goes nowhere
  structured).

Allowed (these go through writers the test harness can capture):

- `fmt.Fprint*` to a writer (`cmd.OutOrStdout()`, `os.Stderr` in
  bootstrap-only paths).
- `slog.Info / Debug / Warn / Error` directly. The wrapper at
  `internal/slogger` only handles initialization (`Setup`);
  there is no banned slog method.

Exempt files (audit walks past them):

- `_test.go` files -- tests can use any logging shape.
- `scripts/`, `research/`, `vendor/`, `node_modules/`.
- `cmd/version.go`, `cmd/completion.go` -- bootstrap output before
  the slog system is initialized.

### Audit tool

`make slog-audit` greps the tree, prints per-package counts and the
first 30 offending call sites, exits non-zero on hits. CI runs this
on every PR. Local runs let contributors find their own violations
before pushing.

The audit is a first-line smell test, not an authority. A clean audit
means no banned patterns leaked through; it does not mean coverage is
sufficient. Every file passing the audit can still be drastically
under-logged. When you find yourself thinking "this is enough logging,"
it is not. Add more events: every branch taken, every value chosen,
every retry, every fallback, every silent default, every conditional
short-circuit, every loop iteration that touches state. The cost of
an extra `slog.Debug` is bytes; the cost of a missing one is a
debugging session that ends with "we have no idea what happened."
Default to over-logging; trim only when an event proves itself
permanently useless across many real incidents.

## TUI QA harness (`clyde-tui-qa`)

The **`cmd/clyde-tui-qa`** binary drives the **real** `clyde` TUI (not the in-memory `SimulationScreen` tests). Use it to iterate on UX and flows the way a user would: launch, read the screen, send keys or mouse bytes, repeat.

### Drivers

| Driver  | Role                                                                                                                              |
| ------- | --------------------------------------------------------------------------------------------------------------------------------- |
| `tmux`  | Fast; `tmux` must be on `PATH`. Good default for agents and CI-style smoke.                                                       |
| `pty`   | In-process PTY plus `vt10x` parsing; canonical terminal semantics; use the **`repl`** subcommand (single long-lived process).     |
| `iterm` | Real iTerm2 via AppleScript (macOS only). Multi-invocation subcommands need **`--iterm-session-id`** from `session-start` stdout. |

### Typical agent loop

1. Build: `make tui-qa` or `make build build-tui-qa`.
2. Optional hermetic tree: `dist/clyde-tui-qa env-print --isolated /tmp/clyde-tuiqa-$$` and `source` / export those lines, or pass **`--isolated`** on **`repl`** / **`session-start`** (sets XDG and `HOME` under that root).
3. Optional **`--seed`** with **`--isolated`** to create one demo session row (`tuiqa-demo-01`).
4. Run **`repl`** with **`--disable-daemon`** (default) unless you intentionally test the daemon.
5. In **`repl`**, use **`capture`** to dump the pane, **`send`** with tmux-style tokens (`Enter`, `Tab`, `C-c`, etc.), **`raw`** with hex for SGR mouse or escapes, **`sleep MS`**, **`quit`**.

### One-shot tmux workflow

`session-start` prints the tmux session name. Pass **`--session`** to **`session-capture`**, **`session-send`**, **`session-stop`** in follow-up invocations.

### iTerm follow-up invocations

`session-start` prints the **iTerm session id** (AppleScript). Pass **`--iterm-session-id`** (or **`CLYDE_TUIQA_ITERM_ID`**) to **`session-capture`** / **`session-send`** / **`session-stop`**.

### Regression tests

Keep structural UI regression in **`internal/ui/*_test.go`** (standard `testing` tests and `tcell.SimulationScreen`). The harness is for **live** subprocess and terminal behavior.
