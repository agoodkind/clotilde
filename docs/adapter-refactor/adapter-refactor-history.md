# Adapter refactor: history log

Append only record of completed adapter refactor work. Newest entries on top.

## 2026-04-29

- Finished the remaining root-cruft cleanup after Plan 6: collect-mode assembly now merges normalized `render.Event` values directly in Anthropic and Codex, `internal/adapter/server_streaming.go` is deleted, and the surviving shared SSE shim lives in a small root helper file.
- Deleted `internal/adapter/tooltrans/` completely by inlining the last sentinel cleanup helpers into `internal/adapter/codex/`, and moved the actionable stream-error test coverage into `internal/adapter/anthropic/backend/stream_errors_test.go`.
- Removed the last chunk-first provider compatibility surface: `provider.EventWriter` no longer exposes `WriteStreamChunk(...)`, shared provider writers now keep chunk rendering private, and the collect path no longer buffers rendered chunks.
- Finished Plan 6 render ownership: both live providers now emit normalized `render.Event` values through `provider.EventWriter`, shared provider writers own event rendering plus OpenAI finish/usage framing, and the root Codex dispatcher no longer hand-builds terminal stream chunks.
- Removed the last production compatibility wrappers from the render migration: deleted `RunTranslatorStream(...)` from the Anthropic backend, deleted `ParseSSE(...)` from Codex, moved the remaining test callers onto event-native helpers, and deleted the dead `internal/adapter/stream_chunk_convert.go` helper.
- Removed the Anthropic `claude -p` fallback entirely: deleted `internal/adapter/anthropic/fallback/`, deleted `internal/adapter/anthropic_bridge.go`, removed the fallback branch from `internal/adapter/server.go` and `internal/adapter/server_backend_contract.go`, and collapsed `internal/adapter/anthropic_provider_dispatch.go` to direct provider-error surfacing with no fallback escalation.
- Deleted the last fallback-only model and test plumbing: removed `BackendFallback`, `CLIAlias`, fallback preflight/provider-stats handling, and fallback lock-in tests; `NewRegistry` now rejects `[adapter.fallback]`, fallback model backends, and fallback logprobs knobs as unsupported.
- Kept `internal/adapter/oauth_handler.go` narrowed to Anthropic request-building and identity assembly only, with no fallback runtime ownership left under the root adapter package.
- Finished the Anthropic provider cutover entry path: `internal/adapter/server_backend_contract.go` now routes `BackendAnthropic` through `internal/adapter/anthropic/provider.go` and `internal/adapter/anthropic_provider_dispatch.go` for both collect and stream instead of the old top-level Anthropic dispatcher path.
- Extended the provider boundary for Anthropic by wiring collect and stream callbacks into `anthropic.Provider.Execute`, adding `provider.Result.FinalResponse`, and teaching the provider stream writer to surface native Anthropic/OpenAI SSE error envelopes without losing the existing wire shape.
- Collapsed Anthropic fallback to one live source of truth in `internal/adapter/anthropic_bridge.go`: the server now owns the fallback request build, transcript-resume preparation, collect/stream execution, and fallback completion/failure logging directly against `internal/adapter/anthropic/fallback/`, and the duplicate backend-owned fallback runtime entrypoints were removed.
- Removed the stale top-level Anthropic dispatcher entrypoints from `internal/adapter/anthropic/backend/backend.go`; the file now only retains shared fallback error helpers used by the live provider path.
- Trimmed root Anthropic cruft by deleting unused bridge-style wrappers that no longer had live callers after the provider cutover, while keeping `oauth_handler.go` focused on Anthropic request building, identity derivation, and cache-usage logging.
- Split cache-usage logging out of `internal/adapter/oauth_handler.go` into `internal/adapter/cache_usage.go`, leaving `oauth_handler.go` narrowed to Anthropic request-building and identity assembly helpers only.
- Added or updated Anthropic provider lock-in coverage in `internal/adapter/anthropic/provider_test.go`, and reran adapter verification successfully: `go test ./internal/adapter/...`, `make staticcheck`, `make build`, and `make test` all passed on 2026-04-29 after the cutover cleanup.
- Recorded fresh real Anthropic rate-limit evidence from `~/.local/state/clyde/anthropic.jsonl`: multiple `anthropic.ratelimit` events for `claude-opus-4-7` on 2026-04-29 around 21:30 PDT with `status=429`, `anthropic-ratelimit-unified-status="rejected"`, `anthropic-ratelimit-unified-7d-status="rejected"`, and long `retry_after` values, which validates the live classifier/error-envelope path against current upstream headers.

## 2026-04-27

- Reconciled Phase 5 and Phase 6 Codex todo lists against actual code state. Work marked complete includes Codex request builder extraction, response assembly behind `MergeChunks(...)`, SSE parsing and tool-call reconstruction, and finish-reason normalization through shared `finishreason` helper.
- Reconciled plan-level Codex app parity and Anthropic backend workstreams. Codex parity slices now include HTTP request serialization, websocket `response.create` contract, service-tier mapping, `max_completion_tokens` passthrough, warmup via `generate=false`, previous-response reuse, and transport telemetry. Anthropic parity includes notice/error classification, request shaping with thinking/billing/compact policy, response assembly, stream translation, usage/finish handling, and fallback escalation.
- Updated Phase 1 plan doc to narrow stale "sweep everything" Cursor semantics todo to actual remaining gaps: model normalization via provider resolver, subagent/background request classification via tool metadata, and cleanup of substring heuristics. Added research references from `research/claude-code-source-code-full` and `research/codex/codex-rs`.
- Started Phase 1 implementation slice on Cursor semantics: made plan mode and request-path detection explicit on `cursor.Request` instead of inferring from prompt-text substrings; updated `DetectMode` and mode-classification tests to use derived fields.

## 2026-04-26

Heavy day. All work listed below under this date.

### Phase 2: Anthropic request shaping

- Extracted Anthropic request shaping into `internal/adapter/anthropic/backend/request_builder.go`, including `callerSystem`, billing header construction, prompt-cache system block, microcompact, per-context betas, effort, and thinking policy.
- Added backend-owned `BuildRequest(...)` entrypoint; root `oauth_handler.go` now thin facade that gathers config and delegates.

### Phase 3: Anthropic response handling

- Removed root `runOAuthTranslatorStream` wrapper; `internal/adapter/anthropic/backend/response_runtime.go` now calls `RunTranslatorStream(...)` directly against backend-owned `StreamClient`.
- Moved `mergeOAuthStreamChunks` and Anthropic response assembly into `internal/adapter/anthropic/backend/response_merge.go`; deleted `internal/adapter/server_response.go`.
- Moved usage mapping (`UsageFromAnthropic` + tracker rollups) to backend response path; root now supplies tracking dependency only.
- Moved stream error shaping, empty-stream handling, and notice header handling out of root wrappers; backend now owns error envelope vs. actionable-text decision.
- Anthropic notice/error classification workstream completed: added `internal/adapter/anthropic/classify.go` with pure `Classify(resp, err)` function returning one of four classes plus header flags; inventoried header readers; plumbed classifier through error path with typed `*UpstreamError` at call sites; added native OpenAI error envelope via `EmitStreamError(...)` in `internal/adapter/openai/sse.go` mapping classes to error shapes; routed typed upstream failures through status-based routing instead of misleading prose; comprehensive lock-in tests cover 429 retryable, 5xx retryable, fatal 4xx, `200 + overage_rejected`, `200 + allowed_warning`, header casing tolerance, and envelope shape.

### Phase 4: Backend-owned Anthropic fallback

- Moved escalation decision logic into `internal/adapter/anthropic/backend/`; `Dispatch(...)` owns OAuth-to-fallback choice.
- Moved explicit `claude -p` subprocess driver under `internal/adapter/anthropic/fallback/`; config resolution now package-owned via `fallback.FromAdapterConfig(...)`.
- Moved fallback request construction (OpenAI message flattening, tool mapping, tool choice, session IDs) into `internal/adapter/anthropic/fallback/`.
- Moved fallback response mapping (result-to-chat-completion, stream replay, live event chunks, usage/tool-call conversion) into `internal/adapter/anthropic/fallback/`.
- Moved fallback execution wrappers and transcript-resume mechanics into `internal/adapter/anthropic/fallback/`; root now calls `CollectOpenAI(...)`, `StreamOpenAI(...)`, `PrepareTranscriptResume(...)`.
- Moved fallback HTTP/SSE writing, request/cache/completion logging, and terminal cost accounting into `internal/adapter/anthropic/backend/fallback_runtime.go`.
- Moved root fallback validation, unsupported-field logging, request construction, response-format prompt injection, and transcript-resume policy behind backend dispatcher boundary.
- Deleted `internal/adapter/fallback_handler.go`; explicit fallback now routes through `Server.HandleFallback(...)` calling `anthropicbackend.HandleFallback(...)` directly.

### Phase 5: Codex request shaping

- Deleted `internal/adapter/codex_handler.go` (500+ lines of inline request shaping, parser helpers, write-intent logic). Direct websocket/HTTP selection moved to `internal/adapter/codex/direct_runtime.go`; managed app session policy moved to `internal/adapter/codex/managed_runtime.go`; app event parsing moved to `internal/adapter/codex/transport_app.go`.
- Live root calls now delegate to Codex backend entrypoints; root Codex files now provide dependency plumbing for auth/config/session construction.
- Moved request-builder and parser assertions into `internal/adapter/codex/request_builder_test.go` and `internal/adapter/codex/parser_test.go`; deleted `internal/adapter/codex_handler_test.go`.

### Phase 6: Codex response and stream handling

- Moved Codex SSE parsing and tool-call reconstruction into `internal/adapter/codex/protocol.go` with `ParseTransportStream(...)` entrypoint in `internal/adapter/codex/events.go`.
- Moved final response assembly into `internal/adapter/codex/respond.go` via `MergeChunks(...)`; Codex no longer relies on Anthropic-oriented `mergeOAuthStreamChunks`.
- Finished Codex finish-reason normalization through shared `internal/adapter/finishreason` helper mapping terminal states to OpenAI finish reasons.
- Moved tool-call parser tests into `internal/adapter/codex/parser_test.go`; deleted root parser assertions.

### Codex app parity workstream

- Added `internal/adapter/codex/request_builder.go` forwarding `max_completion_tokens` from surface and mapping service tier (`fast -> priority`, `flex` preserved) with lock-in tests `TestBuildCodexRequestPassesThroughMaxCompletionTokens`, `TestBuildCodexRequestMapsFastServiceTierToPriority`, `TestBuildCodexRequestPreservesFlexServiceTier`.
- Encapsulated direct Codex HTTP SSE transport in `internal/adapter/codex/transport_http.go`; root `runCodexDirect` now delegates POST/SSE to `codex.RunHTTPTransport`.
- Added `internal/adapter/codex/transport_ws.go` with Codex websocket wire contract: `response.create`, request conversion from `HTTPTransportRequest`, `generate=false` warmup, `previous_response_id` reuse. Dial uses `github.com/gorilla/websocket`; config flag `adapter.codex.websocket_enabled` (default false); `426 Upgrade Required` fallback via `ErrWebsocketFallbackToHTTP`. Event translation to synthetic `event:` / `data:` frames feeds existing `ParseSSE`. Lock-in tests cover base serialization, warmup, previous-response reuse, 426 fallback, and live stream.
- Added `internal/adapter/codex/protocol.go` capturing completed `response.id` and `response.output_item.done` payloads; `continuation.go` stores daemon-local continuation state; `runCodexDirect` applies `previous_response_id` and incremental input slice on eligible turns. Tests cover first-turn misses, prefix deltas, Cursor replay tails, server-baseline reuse, baseline mismatch, config/model mismatch, explicit invalidation, websocket id capture, item capture, prewarm connection reuse, and prewarm retry.
- Added websocket header and turn-state parity: conversation id as `x-client-request-id`; emits `session_id`, `x-codex-window-id`, `x-codex-installation-id`, beta header; captures `x-codex-turn-state`; exposes `has_turn_state` in telemetry. Tests cover header construction, live handshake identity, turn-state capture.
- Added `internal/adapter/codex/capabilities.go` with three distinct context windows: advertised, observed (active transport), effective safe (90% observed). `/v1/models` surface overlays Codex truth: HTTP disabled reports observed `272000` + effective `244800` for `gpt-5.4` / `gpt-5.5`; websocket preserves advertised alias window. Lock-in tests `TestCapabilityReportForModelUsesObservedHTTPContextForCodexResponses`, `TestCapabilityReportForModelPreservesAdvertisedContextWhenWebsocketEnabled`, `TestApplyCapabilityReportOverridesContextFields`, `TestCodexCapabilityOverlayAppliesTransportAwareContextTruth`.
- Added `internal/adapter/codex/output_controls.go` moving output-shaping knobs: `BuildOutputControls` owns `max_completion_tokens` passthrough; `ServiceTierFromMetadata` owns `service_tier` mapping. Lock-in tests `TestBuildOutputControlsPassesThroughMaxCompletionTokens`, `TestServiceTierFromMetadataMapsFastToPriority`, `TestServiceTierFromMetadataPreservesFlex`, `TestServiceTierFromMetadataIgnoresInvalidMetadata`.
- Added `internal/adapter/codex/service_tier.go` and `internal/adapter/codex/ws_headers.go` for websocket parity headers; emits beta header and `x-client-request-id`; `x-codex-turn-state` capture/replay. Lock-in tests confirm parity headers and tier mapping.
- Consolidated HTTP and websocket parsing behind `internal/adapter/codex/events.go`: added `ParseTransportStream(body, requestID, alias, log, emit)` constructing `EventRenderer` and driving `ParseSSE` for both transports. Both `transport_http.go` and `transport_ws.go` now delegate rather than instantiate inline.
- Added Codex-backend transport telemetry in `internal/adapter/codex/telemetry.go`: shared `TransportTelemetry` logs upstream model, chosen transport, service tier, `max_completion_tokens`, prompt-cache, client-metadata, input/tool counts, websocket warmup, previous-response reuse, HTTP fallback, context-window failure. Both transports emit one structured `adapter.codex.transport.prepared` event instead of separate logs. Lock-in test `TestLogTransportPreparedIncludesParityFields`.
- Added explicit parity test matrix: `TestCodexTransportParityMatrixSerialization` covers HTTP serialization, websocket `response.create`, service tier, `max_completion_tokens`, warmup, previous-response reuse; `TestBuildCodexRequestParityMatrixPreservesAliasIntent` covers alias-to-upstream preservation and request-surface parity knobs.
- Documented "Codex 1M characterization note": aliases may advertise `1M`, but active HTTP Codex path characterized in-repo as `272000 observed / 244800 effective safe` for `gpt-5.4` and `gpt-5.5`; websocket preserves advertised window only because measured limit not yet recorded.

### Phase 7: Backend-own Codex direct/app/fallback policy

- Moved transport selection and direct degradation policy fully behind Codex helpers: `internal/adapter/codex/escalation.go` owns `ShouldEscalateDirect(...)`; `internal/adapter/codex/selection.go` owns `resolveTransportSelection(...)`. Root no longer owns `codexShouldEscalateDirect`; `codex.Dispatcher` interface no longer exposes policy hook.
- Moved one-shot app fallback RPC bootstrap and event loop into `internal/adapter/codex/transport_app.go` via `codex.RunAppFallback(...)`; root owns only timeout, prompt building, dependency wiring.
- Centralized direct/app fallback sequencing in `internal/adapter/codex/selection.go` via `resolveTransportSelection(...)`; both `Collect` and `Stream` now route through same selector instead of duplicating inline. Backend-local tests `TestResolveTransportSelectionKeepsDirectOnHealthyPath`, `TestResolveTransportSelectionFallsBackToApp`, `TestResolveTransportSelectionReturnsFallbackError`.
- Removed root-side direct degradation helper; `codex_handler.go` deleted; `internal/adapter/codex/selection.go` calls `ShouldEscalateDirect(...)` directly; bridge method removed from `codex_bridge.go`.
- Added backend-local tests for Codex transport and escalation: `selection.go`, `escalation.go`, `transport_app.go`, `transport_ws.go`, `telemetry.go`, `capabilities.go` coverage plus parity-matrix tests.

### Phase 8: Shrink `tooltrans`

- Moved Anthropic request translation, translated request types, content normalization, and stream translation into `internal/adapter/anthropic/backend/`.
- Removed OpenAI and render aliases from `tooltrans`; callers now import `internal/adapter/openai` and `internal/adapter/render` directly.
- Deleted obsolete `tooltrans` files: `types.go`, `openai_to_anthropic.go`, `stream.go`, `event_renderer.go`, `types_openai_local.go`, `thinking_inline.go`.
- Kept `tooltrans` as sentinel cleanup only.

### Phase 9: Normalize output around one event model

- Audit of `internal/adapter/render/event_renderer.go` against Anthropic and Codex stream paths underway; decision on missing event kinds pending.
- All call sites now import OpenAI stream types from `internal/adapter/openai` and render types from `internal/adapter/render` directly instead of via `tooltrans`.
- Anthropic `StreamTranslator` uses normalized event model internally; contract still returns OpenAI chunks pending next boundary.
- Codex parsing already builds `render.Event` before rendering OpenAI chunks.

### Phase 10: Move tests to match ownership

- Moved Codex tests from `internal/adapter/codex_handler_test.go` into `internal/adapter/codex/request_builder_test.go`, `parser_test.go`, and related Codex package tests; deleted root test file.

### Phase 11: Delete compatibility wrappers

- Remove stale generic fallback assumptions from root dispatch: unsupported backends now fail with explicit `unsupported_backend` error instead of silently spawning generic Claude runner.

### 2026-04-26 Cursor-integration checkpoint

- Request-path classification no longer conflates `Subagent` tool availability with subagent/background request.
- Cursor capability booleans logged explicitly.
- Codex write-intent pruning preserves known Cursor product tools: `Subagent`, `SwitchMode`, `CreatePlan`, `CallMcpTool`, `WebSearch`.

## Phase by phase summary of completed work

### Phase 0: Freeze seams first

Package docs and explicit interfaces defined. Cursor translation, tool vocabulary, mode and prompt contract, foreground model identity, daemon settings model, session summary/detail, and compact settings model all in place.

### Phase 1: Define Cursor and backend execution contracts

Cursor-integration checkpoint deployed. Foreground submit, daemon settings read/write/update, and runtime fallback normalized. Request-path classification no longer conflates `Subagent` with subagent/background requests. Cursor capability booleans logged explicitly. Remaining: model normalization via provider resolver (replacing `adaptercursor.NormalizeModelAlias`), subagent/background classification via tool metadata, cleanup of substring heuristics.

### Phase 2: Extract Anthropic request shaping

Pure Anthropic request mapping helpers extracted to `internal/adapter/anthropic/backend/request_builder.go`. Thinking policy, max-token handling, billing-probe helpers, microcompact policy, caller-system shaping, billing/system-block construction, per-context beta selection, and effort mapping now backend-owned. Root `buildAnthropicWire` thin facade collecting Server config and delegating to `BuildRequest(...)`.

### Phase 3: Extract Anthropic response handling

Core seams landed. `MergeStreamChunks` in `internal/adapter/anthropic/backend/response_merge.go` with neutral `JSONCoercion` contract. Old `server_response.go` deleted. `response_runtime.go` calls `RunTranslatorStream`, `MergeStreamChunks`, runtime notice helpers directly. Removed from dispatcher interface: `MergeAnthropicStreamChunks`, `NoticeForResponseHeaders`, `EmitActionableStreamError`, `RunOAuthTranslatorStream`, `StreamOAuth`, `CollectOAuth`. Anthropic notice/error classification workstream complete with pure classification function, typed error wrapping, native SSE error envelopes, and comprehensive lock-in tests.

### Phase 4: Backend-owned Anthropic fallback

Escalation decision logic, subprocess driver, request construction, response mapping, execution wrappers, HTTP/SSE writing, fallback validation, and transcript-resume policy all moved to Anthropic backend. Root `fallback_handler.go` deleted. Explicit fallback routes through `Server.HandleFallback(...)` calling backend directly.

### Phase 5: Extract Codex request shaping

Codex request construction moved behind `internal/adapter/codex/request_builder.go`. Old root `buildCodexRequest` wrapper and `codex_handler.go` deleted. Tests relocated. Prompt assembly, tool-spec generation, native tool shaping, parallel-tool-calls policy, and tool aliasing centralized in Codex package.

### Phase 6: Extract Codex response and stream handling

Codex SSE parsing, tool-call reconstruction, final response assembly via `MergeChunks(...)` now in Codex package. Finish-reason normalization through shared `finishreason` helper complete. Parser tests relocated; root assertions deleted.

### Phase 7: Backend-own Codex direct/app/fallback policy

Transport selection and direct degradation policy fully backend-owned via `internal/adapter/codex/escalation.go` and `internal/adapter/codex/selection.go`. One-shot app fallback RPC moved to `transport_app.go`. Direct/app/fallback sequencing centralized; both `Collect` and `Stream` route through same selector. Root degradation helper deleted. Comprehensive backend-local test coverage added.

### Phase 8: Shrink `tooltrans`

Anthropic request translation and stream translation moved to `internal/adapter/anthropic/backend/`. OpenAI and render aliases removed. Obsolete files deleted. `tooltrans` kept as sentinel cleanup only.

### Phase 9: Normalize output around one event model

Event renderer audit underway. All call sites now import types directly from `internal/adapter/openai` and `internal/adapter/render` instead of via `tooltrans`. Anthropic and Codex stream paths converging on normalized event model internally.

### Phase 10: Move tests to match ownership

Codex tests relocated to package files. Root tests narrowed to routing and auth concerns.

## 2026-04-27. Codex parity superset (`CLYDE-145`).

A MITM investigation against `codex` CLI (interactive plus `codex exec`) and
Codex Desktop established the wire contract our adapter was missing. Captures
live in `research/codex/captures/2026-04-27/`.

Three empirical findings drove the work:

1. Codex CLI, Codex Desktop, and codex-rs all reuse a single websocket
   connection across many `response.create` rounds and chain
   `previous_response_id` across every frame, with `store: false`. The
   constraint is same-connection lifetime, not `store=true`.
2. Our adapter dialed a fresh ws per Cursor HTTP request and `defer Close`d
   it. We replayed the full ~500 KB Cursor conversation every turn.
3. Both clients send identity headers we were missing: `originator`,
   `x-codex-turn-metadata` (with a `workspaces` block on Desktop), and
   `x-codex-installation-id` from `~/.codex/installation_id`.

Implementation across five commits on main:

- `6cdab8f` Identity headers (`originator: clyde`, typed `TurnMetadata`,
  `LoadInstallationID` reading `~/.codex/installation_id` or persisting a
  clyde uuid).
- `b16a648` `WebsocketSessionCache` with Take/Put/Invalidate/CloseAll
  lifecycle, race-tested.
- `0050745` Cache-aware `RunWebsocketTransport` plus `ComputeDelta`
  suffix-extension matcher. Replaces the cross-process fingerprint approach.
  New telemetry events `adapter.codex.ws_session.*` and
  `adapter.codex.frame.sent`.
- `5e9fe96` Wire the cache into `codex.Provider` with a shutdown hook so
  cached connections do not leak across reload boundaries.
- `a6f4c81` Marshal helper sends `input: []` on warmup (live-discovered
  upstream rejection of the omitted field).

Live verification result on a 3-turn Cursor agent session
(`gpt-5.4`, conversation `e8ab0f01...`):

| metric                                            | gate          | observed                                           |
| ------------------------------------------------- | ------------- | -------------------------------------------------- |
| ws_session.opened per conversation                | 1             | 1                                                  |
| Chained `previous_response_id`                    | yes           | warmup ... 425fec ... 42a150 ... 4fe254 ... 5531ac |
| Delta input rate turn 2                           | < 80% of full | 24% (5/21)                                         |
| Delta input rate turn 3                           | < 80% of full | 12% (3/24)                                         |
| Prompt cache hit rate turn 2                      | high          | 96.5% (cache_read 36864 / prompt 38198)            |
| Prompt cache hit rate turn 3                      | high          | 98.2% (cache_read 37888 / prompt 38572)            |
| `Previous response with id ... not found.` errors | 0             | 0                                                  |
| `adapter.request.failed` events                   | 0             | 0                                                  |

Plan 5b (the fingerprint matcher) is superseded. `CLYDE-147` closes as
"superseded by `CLYDE-145`; cross-process `previous_response_id` reuse with
`store: false` is structurally not supported by the upstream; the persistent
ws session cache plus delta-input matcher is the correct fix."

Follow-ups (not blocking):

- `CLYDE-144` MITM harness still open. Captures from this session seed it.
- `ContinuationStore` remains in tree on the legacy fresh-dial path; remove
  once that path also routes through the cache.
- The `workspaces` block in `x-codex-turn-metadata` is empty for Cursor
  traffic. Populate from the resolved request when Cursor's
  `WorkspacePath` plus git origin and HEAD are available.

## Source documents

- [adapter-refactor.md](/docs/adapter-refactor/adapter-refactor.md): Live plan with phase definitions, detailed todos, and implementation notes.
- [last_agent_progress_apr_26_2026.md](/docs/adapter-refactor/last_agent_progress_apr_26_2026.md): Per-agent daily work log with research, exploration, and implementation context.
