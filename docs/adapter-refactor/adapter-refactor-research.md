# Adapter refactor: research and evidence

Reference companion to `adapter-refactor.md`. The execution checklist lives there. This file holds the underlying product research, protocol notes, and observed wire behavior that informed the refactor.

## Cursor product mapping

### Request shape and model identity

Cursor sends OpenAI-compatible `POST /v1/chat/completions` with a `model` field. Cursor-selected models arrive as native identifiers such as `gpt-5.4`, `gpt-5.5`, `claude-opus-4-7`, and `claude-haiku-4-5-20251001`. The adapter must not expand these into custom flat names such as `clyde-gpt-5.4-1m-medium`.

### Mode and plan-mode semantics

Cursor has an explicit `mode` selector (plan mode, agent mode, foreground mode) and a `SwitchMode` tool that the adapter sees in the tool list. When mode is plan, the Cursor contract expects prefixed instructions from the adapter. The backend-owned request builders should not infer mode from prompt text or tool visibility. Cursor passes no explicit product metadata field for mode; the adapter derives mode from:

1. presence of a `SwitchMode` tool in the request
2. explicit `Subagent` tool presence for background/agent capability detection
3. request-path classification (foreground vs background task vs subagent resume)

### Subagent and resume semantics

Cursor can spawn background tasks and subagents. The adapter should not conflate `Subagent` tool availability in the current request with proof that the current request is a subagent/background path. Request path is classified separately: foreground paths, background-task submits and resumes, and subagent launches and follow-ups each need explicit state management rather than inferring from prompt text.

### Model normalization and parity

Cursor foreground, background-task launch, background-task resume, subagent launch, and subagent follow-up must all use the same model normalization and the same normalized alias internally. The adapter's internal representation (Cursor layer and provider resolver) must not expand into custom flat names such as `clyde-gpt-5.4-1m-medium`.

### Tool vocabulary and MCP conventions

Cursor supplies a set of first-party tools such as `Subagent`, `SwitchMode`, `CreatePlan`, `CallMcpTool`, and `WebSearch`. These are Cursor product tools, not provider-native tools. Codex request shaping has a write-intent filter that should preserve known Cursor tools during request mapping. This is `internal/adapter/cursor/tools.go` responsibility, not scattered through Codex request builders.

### Prompt cache marker placement

Cursor can include `<claude:cache_control type="ephemeral">` markers in user context. The adapter layers should preserve marker placement and semantics when translating requests to backends.

## Codex protocol research

### Responses SSE envelope

Codex Responses API emits streaming events as Server-Sent Events. The SSE frame format is `event: <name>\ndata: <json>\n\n`. Each event has a `type` field in the JSON payload. Live event types include `message`, `function_call`, `local_shell_call`, `custom_tool_call`, and reasoning items. Not all event unions are fully characterized yet.

Response lifecycle fields appear at various points in the envelope: `response.id`, `response.status`, `response.created`, `response.output_items`, `usage.output_tokens_details.reasoning_tokens`, `error.message`, `item_id`, `call_id`, and `summary_index` can appear across different event payloads.

### Websocket transport

Codex also supports a websocket Responses endpoint at `wss://.../v1/chat/completions` with method `response.create`. The websocket accepts a request JSON body and emits server responses as plain JSON messages (not SSE-framed). A `426 Upgrade Required` HTTP response signals websocket fallback to HTTP SSE.

### Response thread continuation

Websocket transport supports incremental response continuation via `previous_response_id`. This allows a subsequent request to extend the previous response by sending only new input items and receiving only new response items. The Codex Rust client stores continuation state per turn in a `ModelClientSession` that tracks `last_request`, `last_response_rx`, websocket connection reuse, and `LastResponse` (upstream `response_id` plus server-returned `items_added`). Clyde's continuation ledger mirrors this: keyed by prompt/conversation identity, storing last request fingerprint, last input sequence, last output item sequence, last `response.id`, and model/config fingerprint.

### App server session management

Codex app servers manage session continuity via RPC. The `initialize` RPC creates a session; `thread_start` begins a conversation thread; `turn_start` executes a turn. Response events flow back as JSON.

### Native tool shaping

Codex request builders accept a `tools` array shaped as tool definitions. Clyde maps Cursor tool names to provider-native names in `internal/adapter/cursor/tools.go` before passing through to Codex request builders.

### Capability reporting

The `/v1/models` endpoint advertises model capabilities including context window. Clyde's current HTTP Codex path does not actually support the advertised `1_000_000` context; measured behavior shows approximately `272000` input context. Websocket path context behavior is not yet independently measured.

### Output controls

Codex request builder accepts `max_completion_tokens`, `service_tier` (mapped from Cursor metadata), and text/verbosity controls (unimplemented in current adapter code).

### Context window evidence

Public docs claim `1M` context for `gpt-5.5` and `gpt-5.4`. Direct probing of Clyde's HTTP Codex path shows approximately `272k` input context in practice. When websocket is disabled, Clyde reports `272000 observed / 244800 effective safe` for those models. When websocket is enabled, Clyde preserves the advertised window pending independent measurement.

## Anthropic notice and error research

### Header interpretation

Anthropic returns `200` with advisory headers when the request succeeded but included warnings. Key headers: `anthropic-ratelimit-unified-status`, `anthropic-ratelimit-unified-overage-status`, `anthropic-ratelimit-unified-overage-disabled-reason`.

A response with status `200` and header `anthropic-ratelimit-unified-overage-status: rejected` is not a failure. The request succeeded and the rate-limit overage was rejected at the edge; the response contains usable content.

### Response classification

Anthropic responses fall into four classes:

1. `ResponseClassFatalError`: `4xx` (except 429) or `5xx` transport failure
2. `ResponseClassRetryableError`: `429` or `5xx` (retryable status codes)
3. `ResponseClassSuccessWithWarning`: `200` with advisory rate-limit or overage headers
4. `ResponseClassSuccessNoWarning`: `200` with no warnings

### Error envelope routing

True upstream failures should surface as native OpenAI error envelopes, not as assistant chat text. Clyde maps the four classes to OpenAI error types: `rate_limit_error/rate_limit_exceeded` (429), `server_error/upstream_unavailable` (5xx retryable), `upstream_error/upstream_failed` (fatal). Success-with-warning cases route through a distinct notice path that does not flatten into assistant prose.

### Classifier outcomes

The classifier lives in `internal/adapter/anthropic/classify.go` and returns both the classification and header-derived flags: `HasOverageRejected`, `HasOverageActive`, `SurpassedThreshold`, `AllowedWarning`.

### Native error envelope

Streaming errors emit an OpenAI SSE error frame via `EmitStreamError(ErrorBody)` in `internal/adapter/openai/sse.go`. Frame format: `data: {"error":{"message":...,"type":...,"code":...}}\n\n`.

### Microcompact policy

Microcompact (extended thinking span inside the streaming window) is Anthropic backend policy. It requires explicit opt-in and sits in `internal/adapter/anthropic/backend/microcompact.go`.

## Claude Code native ingress research

Tracked execution slice: `CLYDE-134` under `EPIC-8`.

### Endpoint override shape

The Claude Code source snapshot points at provider/base-URL override
through env and config such as `ANTHROPIC_BASE_URL`,
`ANTHROPIC_BEDROCK_BASE_URL`, `ANTHROPIC_VERTEX_BASE_URL`, and
`ANTHROPIC_FOUNDRY_BASE_URL`. I did not find a general OpenAI-style
`--base-url` CLI flag for the main inference path. The practical
implication for Clyde is that Claude-native compatibility should target
Anthropic-shaped endpoints rather than try to masquerade as OpenAI
`chat/completions`.

### Main transport shape

The main Claude Code API client in the source snapshot is Anthropic SDK
based. I did not find a native OpenAI client path for inference. This
matches Clyde's current gap: the adapter can already produce
Claude-compatible outbound Anthropic traffic, but the public listener in
`internal/adapter/server_routes.go` still only exposes OpenAI-shaped
ingress routes.

### Model handling implications

Claude Code appears to allow custom model strings, but only after alias
resolution, allowlist checks, and provider validation. Known aliases such
as `sonnet`, `opus`, and `haiku` normalize to canonical Claude-family
ids. The implication for Clyde is that native ingress should advertise
and accept Claude-compatible model ids, not remap these requests to
`gpt-*` aliases.

### Minimal compatibility target

The current best guess for the first useful compatibility slice is:

1. Native `POST /v1/messages`
2. Adjacent token-counting support, likely `/v1/messages/count_tokens`
3. A native model-list surface if Claude Code calls it on the same base
   URL during startup or validation

This remains separate from outbound parity work. `ISSUE-105` and
`ISSUE-124` cover provider ownership and byte-identical claude-cli wire
calls. `CLYDE-134` is the inbound facade slice that lets Claude Code
point at Clyde directly.

## Render ownership evidence

The live provider/runtime boundary now emits normalized `render.Event`
values rather than direct OpenAI chunks.

### Live event-native entrypoints

- Anthropic stream translation now flows through
  `HandleEventEvents(...)` and `RunTranslatorEvents(...)`.
- Codex transport parsing now flows through `ParseSSEEvents(...)`.
- Shared writer ownership lives in `internal/adapter/provider_writer.go`,
  where `WriteEvent(...)` renders events and owns OpenAI finish/usage framing.

### Removed production compatibility wrappers

- The old Anthropic chunk wrapper `RunTranslatorStream(...)` was removed once
  the provider runtime and backend tests no longer depended on it.
- The old Codex chunk wrapper `ParseSSE(...)` was removed once the parser tests
  switched to event-native helpers.
- The dead chunk-conversion helper `internal/adapter/stream_chunk_convert.go`
  was deleted after the Anthropic dispatcher stopped requiring chunk-shaped
  conversion hooks.

## Tooltrans removal evidence

The `internal/adapter/tooltrans/` package once held Anthropic request translation, response stream translation, OpenAI wire aliases, and event-renderer aliases. These have been extracted into backend ownership.

### Deleted files

- `internal/adapter/tooltrans/types.go`
- `internal/adapter/tooltrans/openai_to_anthropic.go`
- `internal/adapter/tooltrans/stream.go`
- `internal/adapter/tooltrans/event_renderer.go`
- `internal/adapter/tooltrans/types_openai_local.go`
- `internal/adapter/tooltrans/thinking_inline.go`

### Remaining content

`tooltrans` now contains only cross-backend sentinel cleanup helpers in `sentinels.go` and their tests. These handle Clyde-injected notice/activity/thinking envelope cleanup that applies across all backends.

### Justification

Backend-specific request translation, SSE translation, and response semantics belong with their backends. Shared OpenAI wire types live in `internal/adapter/openai`, and normalized event rendering lives in `internal/adapter/render`. The package was being used as a hidden backend layer and needed removal to preserve ownership boundaries.

## Source references and live evidence locations

Live adapter logs showing Anthropic response state around the rate-limit notice regression: `~/.local/state/clyde/clyde-daemon.jsonl`

Live Anthropic HTTP responses with headers: `~/.local/state/clyde/anthropic.jsonl`

Local research tree with Cursor, Codex, and Claude source references symlinked: `/Users/agoodkind/Sites/clyde-dev/clyde/research`

Progress log from prior agent checkpoint: `docs/adapter-refactor/last_agent_progress_apr_26_2026.md`

Plan audit and architecture snapshots: `docs/adapter-refactor/adapter-refactor-audit.md`
