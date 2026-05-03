# Structured Logging Contract

This document is the repo-wide logging contract for Clyde. `AGENTS.md` keeps
the working summary; this file is the durable reference for trace/span
correlation and the current observability audit.

## Correlation Model

Clyde carries request correlation through `internal/correlation.Context`.
Every request, RPC, daemon job, provider turn, or background operation that can
accept a `context.Context` should carry this shape:

| Field | Meaning |
| --- | --- |
| `trace_id` | Stable id for the whole user-visible operation or inbound upstream trace. |
| `span_id` | Id for the current Clyde boundary or child operation. |
| `parent_span_id` | Parent span when Clyde entered through HTTP, gRPC, or a nested job. |
| `request_id` | Clyde request/job id, or the best single-operation id available. |
| `cursor_request_id` | Cursor request id when supplied by the client. |
| `cursor_conversation_id` | Cursor conversation id when supplied by the client. |
| `cursor_generation_id` | Cursor generation id when supplied by the client. |
| `upstream_request_id` | Provider request id when supplied by the provider. |
| `upstream_response_id` | Provider response id when supplied by the provider. |

`traceparent` is accepted and emitted where Clyde crosses HTTP or gRPC
boundaries. Clyde treats an inbound `traceparent` span as the parent and creates
a fresh child `span_id` for local work.

## Required Call-Site Behavior

- At ingress, call `correlation.Ensure`, `correlation.FromHTTPHeader`, or
  `correlation.FromIncomingMetadata`, then store the result with
  `correlation.WithContext`.
- For child boundaries, call `corr.Child()` and put the child back on the
  context before logging or dispatching work.
- For gRPC clients, use `correlation.NewOutgoingContext` so metadata carries
  `traceparent` and Clyde correlation headers.
- For HTTP clients or websocket handshakes, set headers with
  `corr.SetHTTPHeaders` or `corr.HTTPHeaders`.
- For `slog`, prefer `InfoContext`, `WarnContext`, `ErrorContext`,
  `DebugContext`, or `LogAttrs` with the active context. The `slogger`
  correlation handler injects missing correlation attributes automatically.
- Explicit event attributes win. If a call site intentionally supplies
  `request_id`, `trace_id`, or `span_id`, `slogger` must not overwrite it.
- Package-level `slog.Info`, `slog.Warn`, and `context.Background()` logs are
  allowed only for bootstrap, process-global loops, panic recovery without an
  originating operation, or older helper APIs that do not accept context yet.

## Boundary Ownership

The boundary that creates or receives work owns the correlation id:

| Boundary | Owner |
| --- | --- |
| Adapter HTTP request | Adapter server ingress. |
| Daemon gRPC request/stream | Daemon correlation interceptors. |
| Daemon client RPC | Daemon client helpers before outgoing metadata. |
| Webapp HTTP request | Webapp server ingress. |
| MCP tool call | MCP handler entrypoint. |
| CLI command | Cobra command `cmd.Context()` or a synthetic command context. |
| Periodic daemon loop | Loop setup creates one job trace per tick/run. |
| Provider request | Provider dispatcher creates a child span for upstream work. |

Do not create unrelated trace ids in lower-level helpers when a caller context
exists. Thread the context down instead.

## Non-Adapter Audit

This audit covers production logs outside `internal/adapter/` as of
2026-05-02. Adapter propagation is tracked separately under
`docs/adapter-refactor/`.

### Already Contextful

These packages already have contextful logging on their main paths and should
inherit trace/span once callers provide correlated contexts:

| Package | Notes |
| --- | --- |
| `internal/search` | Search, embedding, sweep, and LLM calls use `ctx`; one Claude client failure log is contextful. |
| `internal/hook` and `internal/cli/hook` | SessionStart processing accepts `ctx`; CLI hook creates a background context when Cobra has none. |
| `internal/prune` | Prune operations accept `ctx`; daemon scheduling still needs per-tick correlation. |
| `internal/compact/probe`, `count`, `plan`, `runtime`, `summarize` | Main expensive compact operations accept `ctx`. |
| `internal/sessionctx` | Probe and count layers accept `ctx`. |
| `internal/codex` live appserver paths | Appserver runtime logs use `ctx`; store path helpers do not. |
| `internal/claude` lifecycle start/resume paths | Public lifecycle APIs accept `ctx`; some helper subprocess paths still create background contexts. |
| `internal/mitm` capture, launcher, drift runner, proxy env | Main entrypoints accept `ctx`; codegen and drift-file helpers do not. |
| `internal/webapp` | Server lifecycle accepts `ctx`; request ingress should mint/request correlation when handlers are made context-aware. |
| `internal/daemon` RPC handlers | Interceptors install correlation for inbound RPCs; background loops and detached goroutines still need manual threading. |

### Cannot Carry Trace/Span Without API Work

These production logs currently have no operation context available at the log
site. They should stay uncorrelated until their public or internal API accepts a
`context.Context`, or until the owning daemon/CLI boundary wraps the operation
in a synthetic correlated context.

| Package/file | Why it is uncorrelated today | Suggested owner |
| --- | --- | --- |
| `cmd/clyde-staticcheck/main.go` | Standalone tool entrypoint uses no Cobra command context. | Tool main should create a command/job context if needed. |
| `internal/bridge/watch.go` | Watcher API is `Start(dir string)` and panic recovery runs in an internal goroutine. | Daemon bridge watcher should pass a context into watcher construction. |
| `internal/codex/store/paths.go` | Pure path expansion helpers have no caller context. | Callers should resolve paths before logging or helpers should accept `ctx`. |
| `internal/compact/apply.go` | `Apply(in ApplyInput)` and validation helpers have no context. | Compact orchestrator should pass `ctx` into apply. |
| `internal/compact/backup.go` | Backup, snapshot, ledger, restore helpers take session/path only. | Compact apply/undo should thread `ctx` into helper calls. |
| `internal/compact/calibrate.go` | Calibration file helpers take session/model data only. | CLI calibration command should pass `ctx`. |
| `internal/compact/config.go` | Config lookup helper has no context and may be called from several boundaries. | Caller should pass `ctx` or log at caller boundary. |
| `internal/compact/slice.go` | Transcript loading takes only a path. | Compact plan/preview caller should pass `ctx`. |
| `internal/mitm/codegen*.go` | Code generators are file helpers with no runtime operation context. | Codegen command/caller should pass `ctx` if runtime observability matters. |
| `internal/mitm/drift_log.go` | Append-only drift writer has no context. | Drift runner should pass `ctx` to writer. |
| `internal/mitm/launch_profile.go` | Launch-profile detection has no context. | MITM launch boundary should pass `ctx`. |
| `internal/mitm/connect_tunnel.go` | Panic recovery happens in tunnel copy goroutines without a stored parent context. | Tunnel setup should capture and use the parent `ctx`. |
| `internal/notify/log.go` | Notification append helper has no context and writes its own audit file. | Notify caller should pass `ctx` or helper should remain process-level. |
| `internal/outputstyle/outputstyle.go` | Output-style delete helper has no context. | Caller should pass `ctx` when this becomes operationally important. |
| `internal/ui/app.go`, `internal/ui/sigquit_unix.go` | TUI render/timing and signal paths are process-local UI telemetry. | TUI app can keep process-level logs or mint interaction spans later. |
| `internal/util/uuid.go` | UUID helper is intentionally tiny and has no operation context. | Prefer returning errors to callers; do not add synthetic traces here. |
| `internal/slogger/slogger.go` setup failures | Bootstrap happens before logging/correlation initialization. | Leave uncorrelated. |

### Manual Contextful Work Remaining

These areas already have some context but still use `context.Background()` or
detached goroutine logs at meaningful boundaries. They need manual work because
blindly replacing `context.Background()` could tie long-lived loops to a
request lifetime.

| Area | Remaining work |
| --- | --- |
| `cmd/root.go`, `cmd/session_helpers.go`, `cmd/resume.go` | Create a command-level correlated context and pass it to daemon, runtime, MITM, and dashboard helpers instead of fresh backgrounds. |
| `internal/cli/daemon/loops.go` | Create one trace per daemon loop setup and one child span per scheduled tick for prune, OAuth refresh, MITM drift, and MITM listener events. |
| `internal/daemon/client.go` | Preserve caller correlation through connect/retry goroutines and startup nudges instead of creating fresh background contexts. |
| `internal/daemon/server.go` | Thread correlation into discovery scans, global settings watcher, bridge watcher, context usage refresh goroutines, remote-session waiters, and bridge open/close logs. |
| `internal/daemon/run.go`, `binary_update.go`, `transcripts.go`, `live_sessions.go` | Distinguish process-lifecycle logs from reload/live-session child operations, then attach the correct daemon process or RPC context. |
| `internal/mitm/baseline_refresher.go`, `baseline_refresh.go` | Carry the runner context into baseline refresh completion and temp-dir failure logs. |
| `internal/claude/invoke.go`, `pty_invoke.go` | Remove helper-created background contexts where invoke callers already have a lifecycle context. |

## Testing Expectations

Keep `internal/correlation` and `internal/slogger` tests focused on the contract:

- valid trace/span generation
- W3C `traceparent` parent-to-child behavior
- HTTP and gRPC metadata propagation
- `slogger` auto-injection from context
- explicit event attributes taking precedence over context attributes
- concern logs receiving the same correlation fields as the unified log

Do not add production-only test hooks to prove logging behavior. Prefer
capturing the JSONL output from `slogger.Setup` in a temp directory.
