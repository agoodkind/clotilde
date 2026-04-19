# Clyde unified slog standard (P0)

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

| Key            | Meaning                                                  |
|----------------|----------------------------------------------------------|
| `component`    | Subsystem owning the call (`adapter`, `compact`, ...)    |
| `request_id`   | Per-incoming-request correlation id                      |
| `session`      | Clyde session name                                    |
| `session_id`   | Claude session UUID                                      |
| `transcript`   | Absolute path to the JSONL transcript on disk            |
| `model`        | Resolved Claude model name                               |
| `alias`        | Public model alias (clyde-haiku, ...)                 |
| `tokens_in`    | Prompt tokens                                            |
| `tokens_out`   | Completion tokens                                        |
| `duration_ms`  | Operation latency in milliseconds                        |
| `err`          | Error message string (only set on Error)                 |

Levels:

- `slog.Debug(msg, ...)` for inner loops, ticks, polls.
- `slog.Info(msg, ...)`  for state mutations and significant decisions.
- `slog.Warn(msg, ...)`  for recovered or degraded paths.
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

- `_test.go` files  --  tests can use any logging shape.
- `scripts/`, `research/`, `vendor/`, `node_modules/`.
- `cmd/version.go`, `cmd/completion.go`  --  bootstrap output before
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
