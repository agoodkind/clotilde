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

```text
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

Developer tooling: `**cmd/clyde-tui-qa`\*\* drives the real TUI for QA (see the section near the end of this file). It is not part of the default user surface.

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
- Any non-UI-critical work triggered from the TUI must run via a daemon-owned goroutine or daemon-backed async callback. Do not put transcript parsing, filesystem scans, config probing, RPC fan-out, context probes, export aggregation, or similar work directly in TUI draw paths or event handlers.
- TUI code must never block on non-render work. Open the overlay or screen immediately, show progress through the shared loading spinner primitives, and hydrate the view when the goroutine posts results back into the event loop.
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

### Strict Type Hygiene

This repo is pre-alpha. Do not preserve loose compatibility at the cost
of type safety. When a boundary is vague, do the full faithful refactor
and update callers instead of adding another escape hatch.

- Do not introduce `any`, `interface{}`, `map[string]any`, `[]any`, or equivalent open-ended payloads in production code.
- Do not use empty marker structs such as `struct{}` or empty JSON payloads such as `{}` to represent protocol messages, request params, response params, config sections, or domain state.
- All wire, config, RPC, logging, and domain payload types must be deeply and fully enumerated with named structs, typed fields, typed slices, typed maps, and explicit enum-like string types where applicable.
- If upstream data is a union, model the variants explicitly. If only some variants are currently supported, enumerate the supported variants and reject or ignore unsupported ones intentionally at the boundary.
- If JSON must remain partially opaque for a real external contract, isolate that opacity at the smallest possible edge with a named type and a comment that cites the source contract. Do not let raw/dynamic values leak into business logic.
- Prefer generated or researched source-of-truth schemas when available. For Codex, look under `research/codex/` and mirror the fully qualified app-server or Responses protocol types instead of inventing local loose maps.
- Tests should assert the typed shape. Do not build test fixtures with `map[string]any` when the production code has or should have a concrete type.
- Existing loose types are technical debt, not precedent. When touching a loose surface, either replace it with enumerated types in the same change or leave a narrow, explicit follow-up note if the refactor is larger than the active task.

## Architecture

### Core Concept

Clyde is a **thin, non-invasive wrapper**. It:

- Generates UUIDs and stores name→UUID mappings in `.claude/clyde/sessions/<name>/metadata.json`
- Invokes `claude` CLI with the mapped UUID
- Never modifies Claude Code itself

### Session Structure

Each session is a folder in `.claude/clyde/sessions/<name>/`:

```text
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

**Global config** (`internal/config/load.go`, `LoadGlobalOrDefault`): read from `$XDG_CONFIG_HOME/clyde/` (default `~/.config/clyde/`). `**config.toml` is preferred; `config.json` is used if TOML is absent.\*\* `SaveGlobal` writes TOML only.

The `Config` struct in `internal/config/config.go` includes `defaults`, `profiles`, `logging`, `adapter`, `search`, and other sections. `**profiles` exists in the on-disk schema. No production code path reads `cfg.Profiles` outside config tests today\*\*, so do not document a `clyde` CLI that applies a profile by name until that wiring lands.

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

- `**startup`\*\*: New sessions. Outputs session name and context, saves transcript path.
- `**resume**`: Resuming or fork flows. Outputs context when metadata has it.
- `**compact**`: Session compaction. Defensive handler (Claude Code does not currently create a new UUID for `/compact`, but we handle it anyway in case behavior changes).
- `**clear**`: Session clear. Updates metadata with new UUID and preserves old UUID in `previousSessionIds`.

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

1. Hook calls `session.AddPreviousSessionID()` to update metadata:

- Appends current UUID to `previousSessionIds` array (idempotent)
- Updates `sessionId` to new UUID

1. Session name persists across multiple `/clear` operations

**Note on `/compact`:** Currently, Claude Code does NOT create a new session UUID when `/compact` is run (only `/clear` does). However, the hook defensively handles `source: "compact"` identically to `source: "clear"` in case Claude Code's behavior changes in the future.

**Context loading:**

- Hook outputs context to stdout which gets automatically injected by Claude Code
- Session name is always output if available (e.g. "Session name: my-feature")
- Session context from metadata is output if set (e.g. "Context: working on GH-123")
- Hooks use os.Stdin piping to read JSON input from Claude Code

### Claude Code Path Conversion

Claude Code stores project data in `~/.claude/projects/` with paths like:

```text
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

### OpenAI compatible adapter **\*IN FLUX THIS SECTION IS STALE\*\***

The daemon optionally hosts an OpenAI Chat Completions v1 HTTP surface under `internal/adapter/`. Incoming `model` strings resolve through a registry built from `[adapter]` and `[adapter.models]` in config (`internal/adapter/models.go`). Backends include direct Claude, Anthropic HTTP, configured shunts, and the local `claude` CLI fallback (`BackendFallback`). See `reasoning_effort` and family `efforts` in the adapter packages for how effort maps to wire format. Streaming and non streaming paths exist; tool calling, images, and embeddings policies are enforced in the dispatcher. There is no checked in `docs/openai-adapter.md`; read the code and config schema as the source of truth.

### Daemon reload behavior

`clyde daemon reload` is a zero-bind-gap binary handoff, not a
blind process restart. Keep these semantics intact when changing
`internal/daemon/run.go`, `internal/adapter/`, or `internal/webapp/`:

- Reload always re-execs the daemon's current `os.Executable()` path.
  The client does not send an executable path.
- The old daemon passes daemon-owned listener file descriptors to the
  child with `exec.Cmd.ExtraFiles`: gRPC Unix socket, adapter TCP
  listener when enabled, and webapp TCP listener when enabled.
- Listener addresses are part of the handoff contract. If adapter or
  webapp host/port changed, reload must reject with a clear "full
  restart required" error rather than rebinding.
- The child starts public listeners from inherited FDs first, reports
  readiness over the reload pipe, then waits to acquire
  `daemon.process.lock` before running exclusive background loops.
- A reload child may serve the inherited daemon socket before it owns
  `daemon.process.lock`, but it must not be allowed to initiate
  another reload during that window. `ReloadDaemon` must reject with
  `FailedPrecondition` until the generation owns the process lock, or
  repeated reload calls can create a parent/child/child generation
  chain.
- After the child is healthy, the old daemon immediately stops
  accepting public traffic: adapter and webapp listener references
  are closed, keepalives are disabled, idle keepalive connections are
  closed, and active HTTP handlers may finish until the short HTTP
  drain deadline. After that deadline, the old generation force-closes
  any remaining adapter/webapp HTTP connections. This is intentional.
  Existing TCP keepalive connections cannot be transferred to the new
  Go HTTP server state, and leaving them reusable lets clients such as
  Cloudflare Tunnel/Cursor keep sending new requests to stale code. New
  TCP connections after reload completion must be accepted only by the
  child generation.
- Existing gRPC streams stay on the old process until they finish
  because reload uses `grpc.Server.GracefulStop()`. This includes
  in-flight compaction preview/apply streams. The gRPC drain still has
  a long hard cap so stale streaming clients cannot keep old daemon
  generations and `daemon.process.lock` alive indefinitely.
- After child readiness, the old generation stops exclusive subsystems
  and releases `daemon.process.lock` before continuing gRPC drain. This
  lets the child become the reload-capable owner while old accepted
  streams finish. Reload clients keep `daemon.reload.lock` and retry
  `FailedPrecondition` by reconnecting, so concurrent reload calls
  serialize and the newest lock-owning generation performs the next
  reload.
- Preserve active session runtime dirs while the old process drains so
  wrappers and remote-control sockets can reacquire against the child.
- Concurrent reloads should remain serialized by the reload lock; a
  queued reload reconnects to the latest daemon generation, giving
  last-writer-wins behavior.

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

## Structured logging and observability

Use structured `log/slog` logging across the repo, and treat observability as a product feature: logs should be detailed enough to reconstruct what happened, but selective enough that the important events still stand out.

### The goal

The goal is not "log every line" and it is not "log only errors". The target is:

- one event at every meaningful boundary: process start, request start, external call, state mutation, retry, fallback, and completion
- enough fields to explain what the code decided and how long it took
- low-noise handling for hot paths, polling loops, and large payloads

If a production issue would leave you asking "what code path did we take, with which inputs, and why," add logging there. If a path emits the same record hundreds of times per second, reduce or reshape it before adding more.

### Setup

Prefer one repo-local setup path at process start, then use `slog` directly everywhere else.

When a repo uses `goodkind.io/gklog`, `gklog.New` is the generic factory. It can tee logs to JSON stdout, a rotating JSON file, a rotating text file, and optional email alerts. It annotates every record with `build` from `goodkind.io/gklog/version`. It returns `(*slog.Logger, io.Closer, error)`, and the caller is responsible for calling `slog.SetDefault(logger)` if the repo wants package-level `slog.Info(...)` calls to use that logger.

Typical pattern:

```go
import (
    "log/slog"

    "goodkind.io/gklog"
)

func setupLogging() (func(), error) {
    logger, closer, err := gklog.New(gklog.Config{
        JSONLogFile:   "/var/log/myapp/app.jsonl",
        DisableStdout: true,
        JSONMinLevel:  "info",
    })
    if err != nil {
        return nil, err
    }
    slog.SetDefault(logger)
    return func() {
        _ = closer.Close()
    }, nil
}
```

Notes for `gklog` semantics:

- `DisableStdout: false` enables JSON logs on stdout. That is useful for systemd, journald, containers, and platforms that scrape stdout.
- `DisableStdout: true` is usually the right choice when stdout is part of the program's user-facing or machine-readable contract.
- `JSONLogFile` and `TextLogFile` are optional and can be enabled together.
- `Rotation` applies to the file handlers. `gklog` uses locked writers so multiple processes writing the same log path do not interleave records.
- `EmailSend` plus `EmailTo` enables an email alert handler with threshold and cooldown controls. Use this for rare operator-facing alerts, not routine app flow.
- `JSONMinLevel` controls the JSON stdout and JSON file handlers. Empty or unknown values default to `debug` in `gklog`.

If the repo wraps `gklog` in its own setup package, keep the wrapper thin. The wrapper should choose paths, levels, and outputs, then install the logger. Business code should still call `slog` directly.

### Request-scoped fields

Attach stable per-request or per-job fields once at the boundary, then store the logger on `context.Context`.

```go
import (
    "log/slog"
    "net/http"

    "goodkind.io/gklog"
)

func handleRequest(w http.ResponseWriter, r *http.Request) {
    requestID := newRequestID()
    log := slog.Default().With(
        "request_id", requestID,
        "component", "http",
    )
    ctx := gklog.WithLogger(r.Context(), log)

    gklog.LoggerFromContext(ctx).InfoContext(ctx, "http.request.started",
        "method", r.Method,
        "path", r.URL.Path,
    )

    // ... pass ctx through the stack ...
}
```

`gklog.WithLogger(ctx, log)` stores the logger on the context. `gklog.LoggerFromContext(ctx)` returns that logger, or `slog.Default()` when none was stored. `gklog.L(ctx)` is a short alias.

### What to log **THIS LIST IS NON-EXHAUSTIVE**

Prefer events at these points:

- process lifecycle: startup, shutdown, config load, migration, background worker start and stop
- request or job lifecycle: accepted, validated, dispatched, completed, canceled, timed out
- external boundaries: database calls, filesystem writes, subprocesses, RPC, HTTP, queues
- control-flow decisions: retries, fallbacks, cache hits and misses, feature-flag branches, degraded mode
- state changes: create, update, delete, enqueue, prune, compact, reconcile
- failures: returned errors, partial failures, recovered panics, dropped work

Prefer fields that make those events queryable and comparable:

| Key            | Meaning                                                     |
| -------------- | ----------------------------------------------------------- |
| `component`    | Top-level subsystem (`api`, `worker`, `store`, `adapter`)   |
| `subcomponent` | Narrower emitter inside that subsystem                      |
| `request_id`   | Correlation id for one incoming request or job              |
| `trace_id`     | Distributed trace or upstream correlation id when available |
| `session`      | Human-oriented session, tenant, or job name when relevant   |
| `session_id`   | Stable UUID or internal identifier when relevant            |
| `model`        | Resolved model or backend choice when applicable            |
| `duration_ms`  | Elapsed latency in milliseconds                             |
| `attempt`      | Retry number or delivery attempt                            |
| `count`        | Item count for batch work                                   |
| `path`         | File or route involved in the operation                     |
| `status`       | Outcome summary (`ok`, `retry`, `timeout`, `dropped`)       |
| `err`          | Error value on `Warn` or `Error` events                     |

Use the event message as the event name. Prefer a stable dot-separated form such as `http.request.completed`, `worker.job.retried`, or `store.snapshot.loaded`.

### Levels and noise budget

Use levels to keep logs useful:

- `slog.Debug` for hot-path detail, loop internals, polls, and verbose diagnostic breadcrumbs
- `slog.Info` for meaningful lifecycle events, state changes, and one-per-operation summaries
- `slog.Warn` for degraded paths, retries, partial failures, and unexpected-but-recovered conditions
- `slog.Error` for failures that affect correctness, availability, or operator action

Avoid unusable logs by shaping noisy paths instead of deleting observability:

- Emit one `Info` summary for the whole operation, and keep per-step detail at `Debug`.
- Log retries individually only when they are rare; otherwise log a final summary with `attempt_count`.
- Do not dump full request or response bodies by default.
- For large payloads, log metadata such as type, size, count, and selected ids.
- For polling loops, emit periodic summaries or state-transition logs instead of one record per tick.
- For high-volume success paths, consider logging only start and completion, with counts and latency.

A good rule is: a healthy steady-state request should usually produce a small handful of `Info` events, not dozens.

### Sensitive or large payloads

Treat request bodies, prompts, tokens, credentials, and personal data as opt-in logging.

If the repo needs body logging, define an explicit policy with modes such as:

- `off`: do not log bodies
- `summary`: log shape only, such as message count, byte size, tool count, or content types
- `whitelist`: log a sanitized subset with truncation and redaction
- `raw`: log the full payload only for tightly controlled local debugging or isolated environments

When using `whitelist`, prefer these safeguards:

- trim long strings to a fixed size
- remove auth headers, secrets, API keys, tokens, cookies, and passwords
- strip large generated schemas or repeated boilerplate
- cap the total logged payload size

### Banned patterns

Do not bypass the structured logger for production diagnostics.

Reject or avoid:

- `fmt.Print`, `fmt.Println`, `fmt.Printf` for operational logging
- `log.Print`_, `log.Fatal_`, `log.Panic\*`from the stdlib`log` package for operational logging

Allowed:

- `fmt.Fprint*` to an explicit writer when the command intentionally produces user-facing output
- `os.Stderr` writes in bootstrap-only failure paths before logging is initialized
- `slog.Debug`, `slog.Info`, `slog.Warn`, and `slog.Error` for normal diagnostics

### Review and audit

If the repo has a logging audit target, keep it strict enough to catch unstructured logging and obvious blind spots. If it does not, add one.

The audit should check at least:

- production files do not use bare stdout logging for diagnostics
- process entrypoints initialize logging before meaningful work starts
- new subsystems emit lifecycle and failure events
- hot paths keep verbose detail behind `Debug` or a similar gate

A clean audit is not proof that observability is complete. Use incident retrospectives, failing tests, and real debugging sessions to decide where the next fields or events should go.
