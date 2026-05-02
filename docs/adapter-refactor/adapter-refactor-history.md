# Adapter refactor completed tasks

This file is a memory aid, not a changelog. It exists so agents do not repeat
completed work.

## Completed

| Issue       | Result                                                                                                                                                                                                |
| ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `CLYDE-138` | Claude session start/resume now routes through `internal/session/lifecycle`; Claude-specific remote-control persistence lives in `internal/claude`.                                                   |
| `CLYDE-144` | Built-in MITM capture, snapshot, diff, codegen, and drift-check tooling landed for provider parity work.                                                                                              |
| `CLYDE-145` | Codex provider now uses direct websocket transport with per-conversation session cache, warmup, chained `previous_response_id`, delta input, identity headers, installation id, and shutdown cleanup. |
| `CLYDE-146` | Anthropic rate-limit classifier was validated against real OAuth-bucket 429 evidence and typed SSE error-envelope coverage.                                                                           |
| `CLYDE-147` | Cancelled. The old cross-process continuation hit-rate issue was superseded by `CLYDE-145`.                                                                                                           |
| `CLYDE-148` | Cancelled. Reconnect telemetry validation was superseded by the `adapter.codex.ws_session.*` and `adapter.codex.frame.sent` signals from `CLYDE-145`.                                                 |
| `CLYDE-149` | Anthropic provider cleanup landed; root fallback and bridge paths are gone, and provider-owned dispatch handles collect and stream.                                                                   |
| `CLYDE-156` | Malformed Cursor patch-hunk handling closed outside the active adapter plan.                                                                                                                          |

## Deleted Or Retired Adapter Paths

- `internal/adapter/anthropic/fallback/`
- `internal/adapter/anthropic_bridge.go`
- `internal/adapter/codex_bridge.go`
- `internal/adapter/codex_app_fallback.go`
- `internal/adapter/codex_runtime.go`
- `internal/adapter/codex_sessions.go`
- `internal/adapter/server_response.go`
- `internal/adapter/server_streaming.go`
- `internal/adapter/stream.go`
- `internal/adapter/stream_chunk_convert.go`
- `internal/adapter/tooltrans/`
- Codex HTTP/SSE and app-server fallback transport files

## Validation Already Done

- Basic Claude CLI native ingress shape was tested with `ANTHROPIC_BASE_URL`
  pointed at a local Anthropic-shaped server. The observed `claude -p` path
  called `POST /v1/messages?beta=true` only.
- Codex websocket reuse was live-verified on a 3-turn Cursor agent session:
  one websocket session opened, chained response ids succeeded, prompt-cache
  hits were high, and no `previous_response_id not found` errors occurred.
- Anthropic 429 handling was validated from existing `anthropic.ratelimit`
  events and lock-in tests.
- Claude Code Snapshot v2 now belongs under the local XDG baseline store, not a
  repo path. Anthropic `wire_flavors_gen.go` still regenerates from a local
  baseline reference, and direct v2 diff is clean.
- `CLYDE-165` now tracks the daemon-owned always-on MITM architecture: rolling
  XDG baselines refreshed from accumulated captures, with drift logged before a
  baseline is replaced.
- Native Anthropic ingress now resolves Clyde aliases to upstream Claude model
  ids before dispatch. A live `/v1/messages` request reached real Anthropic
  OAuth and returned a native 429 from the bucket rather than a local auth or
  alias-resolution failure.
- Long Codex turns on the current daemon show same-connection websocket reuse,
  high prompt-cache reads, and no `previous_response_id not found` errors in
  the inspected window. The same window did not contain a true `Subagent` tool
  request, so `CLYDE-162` remains open for that repro.
- `CLYDE-152` is closed on the current evidence set: websocket reuse held,
  prompt-cache reads stayed high on longer turns, and the inspected window did
  not show continuation-loss regressions.
