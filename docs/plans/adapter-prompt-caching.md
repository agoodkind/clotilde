# Plan: Reduce Cursor-via-adapter token burn with prompt caching

## Context

The adapter under `internal/adapter/` fronts a Claude Max subscription as an
OpenAI `/v1/chat/completions` surface so tools like Cursor can drive Claude
without hitting API billing. Empirically Cursor burns tokens 5–10× faster
than the Claude Code CLI against the same subscription.

Root cause, in order of impact:

1. **No prompt-cache markers.** `grep -r cache_control internal/adapter` returns
   zero matches. Every request re-bills the full prefix (system prompt +
   Cursor's tool schemas + chat history) at 1× input. Claude Code stamps
   `cache_control: {type: "ephemeral"}` on tools, system-prompt blocks, and
   the last message (`src/services/api/claude.ts:3063` `addCacheBreakpoints`,
   `src/utils/api.ts:228`).
2. **No microcompact.** `services/compact/microCompact.ts` rewrites aged
   tool_result blocks to `[Old tool result content cleared]`. No equivalent
   exists in the adapter.
3. **Usage response hides cache efficiency.** `anthropic.Usage`
   (`internal/adapter/anthropic/types.go:150-153`) only has `InputTokens` /
   `OutputTokens`; the adapter drops `cache_creation_input_tokens` and
   `cache_read_input_tokens`, so clients can't see caching work even once it's
   enabled.

Intended outcome: cut input-token bill on a typical multi-turn Cursor session
by **60–85%** with a surgical change to the request-build path. Output tokens
unchanged.

## Savings model

Anthropic billing (as of 2026): cache write = 1.25× input rate; cache read =
0.10× input rate. Prompt caching is GA on claude-4.* — no beta header required
for the 5-minute TTL. The 1-hour TTL still needs
`extended-cache-ttl-2025-04-11` in the beta header.

Baseline per-turn token sources in a Cursor session:

| Component                   | Approx tokens | Changes per turn?      |
| --------------------------- | ------------- | ---------------------- |
| Cursor tool schemas         | 5–10k         | Stable                 |
| System prompt + rules       | 2–8k          | Stable                 |
| @file inlined context       | 2–20k         | Sticky across turns    |
| Prior conversation          | grows         | Append-only            |
| New user message            | 0.2–2k        | New each turn          |

### Worked example — 15-turn session

Assume 12k stable prefix (tools + system + rules), 4k sticky @file context,
2k new user content per turn, 1k assistant reply per turn.

**Without caching (status quo):**

- Per-turn input = 12k (prefix) + 4k (files) + prior conversation + 2k (new).
- Sum over 15 turns: `15 × 16k + Σ(3k × i) for i=0..14` ≈ **555k input tokens**.

**With caching on prefix + tools + last user text block:**

- Turn 1: 16k × 1.25 = 20k write.
- Turns 2–15: 16k × 0.10 = 1.6k read each → 22.4k.
- Plus uncached tail (new content + uncached prior growth): ~90k across 15 turns.
- Total: **~132k input tokens**.

**Reduction: ~76%.** On tool-heavy sessions with large @file chips and long
histories, the win is closer to 85%. Sessions shorter than 3 turns see
roughly break-even (the cache-write surcharge isn't yet amortized).

## Recommended approach

Three coordinated changes in `internal/adapter/`. Ship them together — each
depends on the previous for correctness or observability.

### Change 1 — Add `cache_control` to the wire types

**File:** `internal/adapter/anthropic/types.go`

Add a `CacheControl` struct and optional pointer fields on three existing
types:

```go
type CacheControl struct {
    Type string `json:"type"` // "ephemeral"
    TTL  string `json:"ttl,omitempty"` // "5m" (default) or "1h" (requires beta)
}
```

Add `CacheControl *CacheControl \`json:"cache_control,omitempty"\`` to:

- `ContentBlock` (line 46–55) — for marking text / tool_result blocks.
- `Tool` (line 95–99) — for marking the tool-schemas block. The Explore
  agent's claim that tools don't accept `cache_control` is wrong;
  `src/utils/api.ts:70-74` shows the TS SDK allows
  `cache_control?: {type: 'ephemeral', scope?: 'global' | 'org'}` on tool
  definitions, and `addTools` writes it at `src/utils/api.ts:228-229`.
- Add `CacheCreationInputTokens` / `CacheReadInputTokens` to `Usage`.

No behavior change yet — `omitempty` + pointer = invisible when nil.

### Change 2 — Inject breakpoints in `toAnthropicAPIRequest`

**File:** `internal/adapter/oauth_handler.go` (lines 136–188)

After building `msgs` and `tools` and before the `return anthropic.Request{...}`:

1. **Tools:** set `CacheControl = {Type: "ephemeral"}` on the *last* tool in
   the array. Per Anthropic's caching rules this caches the entire tools
   block plus everything preceding it (system). One marker is enough; more
   markers waste cache slots (Anthropic caps at 4 per request).
2. **Last user message text:** walk `msgs` in reverse, find the last message
   with `Role == "user"`, find its last `Type == "text"` block, set
   `CacheControl = {Type: "ephemeral"}`. Mirrors
   `addCacheBreakpoints` in `src/services/api/claude.ts:3089`.

Skip the marker when the request has fewer than 2 messages (short
conversations don't amortize the write cost) — match CC's
`skipCacheWrite` behavior conceptually.

Do **not** add a third marker on system. Anthropic's 4-marker cap plus
position-based dedupe means the tools marker already covers the system
prefix.

No config flag — caching is strictly additive, costs nothing when prefix
changes between requests, and matches CC's always-on behavior. If future
debugging requires disabling it, `CLYDE_PROBE_DROP`-style env-var gating
is a 5-line add.

### Change 3 — Surface cache usage in responses

**File:** `internal/adapter/anthropic/stream_parse.go`

Extend the inline `streamMessageUsage` struct (lines 10–12) with the two
cache fields. Propagate into `anthropic.Usage` at the existing assignment
sites (line 105–106 for `message_start`, line 176–177 for `message_delta`).

**File:** `internal/adapter/server_response.go` (and wherever
`mergeOAuthStreamChunks` builds the OpenAI `usage` block)

Surface cache tokens in the OpenAI response. OpenAI's chat.completions
response has `prompt_tokens_details.cached_tokens` (official, GA). Map
`anthropic.Usage.CacheReadInputTokens` → `prompt_tokens_details.cached_tokens`.
The cache-write count isn't in OpenAI's schema; log it to the
`adapter.chat.completed` slog event as `cache_creation_input_tokens` so we
can tell caching is working.

Also emit a one-shot slog event per request:

```go
slog.Info("adapter.cache.usage",
    "cache_creation_tokens", u.CacheCreationInputTokens,
    "cache_read_tokens", u.CacheReadInputTokens,
    "input_tokens", u.InputTokens,
    "hit_ratio", float64(u.CacheReadInputTokens)/float64(u.InputTokens+u.CacheReadInputTokens),
)
```

## Files to touch

| File                                                   | What changes                                          |
| ------------------------------------------------------ | ----------------------------------------------------- |
| `internal/adapter/anthropic/types.go`                  | Add `CacheControl` struct + pointer fields on `ContentBlock`, `Tool`; extend `Usage`. |
| `internal/adapter/oauth_handler.go`                    | Stamp markers in `toAnthropicAPIRequest` (~line 161). |
| `internal/adapter/anthropic/stream_parse.go`           | Parse new usage fields.                               |
| `internal/adapter/server_response.go`                  | Map cache_read → `prompt_tokens_details.cached_tokens`; log creation tokens. |
| `internal/adapter/openai.go`                           | Add `PromptTokensDetails` to `Usage` response type.   |
| `internal/adapter/tooltrans/openai_to_anthropic_test.go` | Add test asserting translator leaves caching to oauth_handler (unchanged behavior). |
| New: `internal/adapter/cache_breakpoints_test.go`      | Unit test on `toAnthropicAPIRequest` asserting marker placement. |

## Reused existing utilities

- `anthropic.Request.ExtraBetas` (types.go line 128) already exists and
  dedupes merged flags via `client.go:167-184`. If we later want the 1-hour
  TTL, push `"extended-cache-ttl-2025-04-11"` into `ExtraBetas` — no new
  plumbing needed.
- `anthropic.Message.MarshalJSON` (types.go line 75–92) already collapses
  single-text-block messages to plain strings for cache-prefix stability.
  Verify it preserves the block-array shape when `CacheControl != nil` so
  we don't accidentally drop the marker (likely a 2-line conditional).

## What I am NOT proposing (for this change)

- **Microcompact.** `services/compact/microCompact.ts` is 531 lines of
  stateful tool-result rewriting. Worth pursuing as a follow-up — adds
  another 20–40% savings on tool-heavy sessions on top of caching — but the
  adapter is stateless today and this would require per-client state.
  Defer.
- **Aggressive 4-marker placement** à la `splitSysPromptPrefix`. Adds
  complexity for marginal wins over the simple 2-marker scheme.
- **Config flag.** Caching is always a win; gating it wastes a config
  surface.

## Verification

1. **Unit:** `go test ./internal/adapter/...` — new test asserts a
   synthesized `anthropic.Request` has `CacheControl` set on exactly one tool
   (the last) and on exactly one content block in the last user message.
2. **Integration against live Anthropic:**

   ```
   CLYDE_ADAPTER_TOKEN=$(pass claude/adapter) \
     curl -s localhost:11434/v1/chat/completions \
     -H "Authorization: Bearer $CLYDE_ADAPTER_TOKEN" \
     -d @testdata/long_session.json | jq '.usage'
   ```

   Confirm `prompt_tokens_details.cached_tokens > 0` on the second request
   of a back-to-back pair with the same prefix.
3. **Live Cursor session:** point Cursor at the adapter, run a 10-turn
   session, tail the JSONL log:

   ```
   jq -c 'select(.msg == "adapter.cache.usage")' $XDG_STATE_HOME/clyde/clyde.jsonl
   ```

   Expect `cache_read_tokens` growing across turns and `hit_ratio > 0.5`
   after turn 3.
4. **Regression:** `make slog-audit` + existing
   `server_dispatch_logging_test.go` must still pass.

---

# Phase 2: Make the `claude -p` fallback path first-class

## Context (Phase 2)

Phase 1 (above) shipped prompt-cache markers on the OAuth backend and surfaced
`cache_creation_input_tokens` / `cache_read_input_tokens` on both backends. The
fallback path now reports cache tokens, but three structural decisions in
`internal/adapter/fallback/` still leave tokens on the table and block cleaner
Cursor semantics:

1. **Messages are flattened to a positional arg.** `renderPrompt` in
   `fallback/spawn.go:70-84` joins `[user, assistant, user, …]` into one
   `"user: text\n\nassistant: text"` string and passes it as argv. Claude
   Code's own `addCacheBreakpoints` runs (print mode hits
   `src/cli/print.ts:2147` → same `ask()` → same `addCacheBreakpoints` at
   `src/services/api/claude.ts:3063`), so the single flattened user-turn
   does get a `cache_control` marker — but message-structure bytes shift
   subtly each turn and multi-turn cache reuse is fragile.
2. **No session continuity.** Every request spawns fresh:
   `fallback/config.go:23-24` always sets `cmd.Dir = c.cfg.ScratchDir`, and
   `spawn.go:21-32` never passes `--session-id`, `--resume`, or
   `--continue`. Anthropic's cache is content-hash keyed so some hits still
   happen, but the subprocess rebuilds its own transcript each time.
3. **Tools are prompt-injected.** `fallback/tools.go:27-81` prepends 5–10k
   tokens of tool JSON Schema + "respond with `{\"tool_calls\":[…]}`"
   instructions into `--append-system-prompt`. Cursor's tool schemas ride
   along on every request, model picks them up as literal text, and CC's
   native tool-use path is bypassed entirely.

Claude Code `-p` already supports every knob we need:

- `--input-format stream-json` (flag at `src/main.tsx:3038`, consumed at
  `src/cli/print.ts:587 → getStructuredIO:5199`) accepts NDJSON user
  messages shaped like
  `{"type":"user","session_id":"","message":{"role":"user","content":"…"},"parent_tool_use_id":null}`.
- `--session-id <uuid>`, `-r / --resume`, `-c / --continue` at
  `src/main.tsx:3040+` all exist and keep the same transcript file at
  `~/.claude/projects/<cwd-encoded>/<uuid>.jsonl`.
- `--output-format stream-json` already emits `cache_creation_input_tokens`
  / `cache_read_input_tokens` on the result event
  (`src/entrypoints/sdk/controlSchemas.ts:298-299`, aggregated at
  `src/utils/tokens.ts:49-50`) — Phase 1 already parses these.

Intended outcome: the fallback path behaves like CC's own CLI — proper
message boundaries, cross-turn session continuity, native tools — so cache
hit ratios on fallback match OAuth hit ratios (60–85% on long sessions) and
Cursor's tool_calls flow end-to-end instead of being JSON-parsed out of
response text.

## Phase 2.0 — Verify current baseline (no code)

Before refactoring, confirm what the status quo actually produces. With
Phase 1's logging already in place:

```
jq -c 'select(.msg == "adapter.chat.completed" and .backend == "fallback")
  | {turn: .request_id, stream, tokens_in, tokens_out, cache_read_tokens}' \
  $XDG_STATE_HOME/clyde/clyde.jsonl | tail -20
```

Three outcomes inform the rest:

- **cache_read_tokens > 0 on turn 2+:** structural caching already works;
  Phase 2.1 (stream-json input) is polish, not a token-saver. Deprioritize.
- **cache_read_tokens == 0 across all turns:** prefix bytes are shifting
  between invocations. Phase 2.1 is mandatory.
- **cache_read_tokens > 0 only intermittently:** Phase 2.2 (session id) is
  likely the needed stabilizer.

## Phase 2.1 — Stream-json input via stdin

**Files:**

- `internal/adapter/fallback/spawn.go` (lines 21–84)
- `internal/adapter/fallback/types.go` (extend `Message`)
- `internal/adapter/fallback_handler.go` (`buildFallbackMessages` caller)

**Changes:**

1. Add `--input-format stream-json` to argv in `spawn.go:21-27`.
2. Stop passing the positional-arg prompt. Instead, wire `cmd.Stdin` to a
   `*io.PipeReader`. Rewrite `renderPrompt` → `renderInputStream(msgs)
   io.Reader` that writes one NDJSON line per user turn:

   ```
   {"type":"user","session_id":"","message":{"role":"user","content":"…"},"parent_tool_use_id":null}
   ```

   Assistant turns don't need re-transmission when session continuity is on
   (Phase 2.2 writes them to the transcript); until then, include assistant
   turns as `{"type":"assistant","message":{"role":"assistant",...}}` lines
   so the subprocess has the full history.
3. Extend `fallback.Message` to optionally carry content-parts (text +
   image blocks) and tool_use / tool_result blocks so Cursor's non-text
   content survives. `buildFallbackMessages` already has access to this
   from `NormalizeContent` — plumb it through instead of flattening.
4. Close the pipe writer after the last message so the subprocess reads
   EOF and begins responding.

**Reused utilities:**

- `adapter.NormalizeContent` in `openai.go:370-393` already parses OpenAI
  content-parts; the fallback path currently only uses its string-flatten
  output (`FlattenContent`). Use the structured `ContentKindParts` branch
  directly.

## Phase 2.2 — Session continuity via deterministic `--session-id`

**Files:**

- `internal/adapter/fallback_handler.go` (add session-id derivation)
- `internal/adapter/fallback/types.go` (add `SessionID` to `Request`)
- `internal/adapter/fallback/spawn.go` (pass `--session-id`)

**Changes:**

1. Derive a stable UUID per Cursor conversation: hash `(first user
   message text + model alias + 5-minute epoch bucket)`, format as UUIDv4
   via `util.GenerateUUID` (or similar helper under `internal/util`). The
   5-minute bucket aligns with Anthropic's default cache TTL so the same
   conversation keeps the same ID within one active window.
2. Pass `--session-id <uuid>` on every `claude -p` invocation for that
   conversation. Same ID across turns means CC reuses
   `~/.claude/projects/<cwd-encoded>/<uuid>.jsonl`; assistant replies get
   appended to the transcript without the adapter re-sending them.
3. Prune fallback transcripts older than 1 hour from the scratch-dir's
   projects directory via the existing `internal/prune` machinery so we
   don't leak JSONL files indefinitely.

**Reused utilities:**

- `internal/util.GenerateUUID()` (referenced in CLAUDE.md — already used by
  the fork path).
- `internal/prune` (the daemon already runs prune loops; add a fallback
  transcript sweep).

## Phase 2.3 — Native tools via MCP / `--allowed-tools` (stretch)

Largest effort, highest token payoff on tool-heavy sessions. Defer unless
Phase 2.0 verification shows tool-preamble bloat dominates.

**Files:**

- `internal/adapter/fallback/tools.go` (stop emitting preamble; emit MCP
  config JSON instead)
- `internal/adapter/fallback/spawn.go` (add `--mcp-config <path>`,
  `--permission-mode bypassPermissions`)
- New: `internal/adapter/fallback/mcp_config.go` (generate per-request MCP
  config exposing Cursor tools as MCP tools)

**Changes outline:**

1. Walk Cursor-provided `req.Tools` and render an MCP server config that
   exposes each tool as an MCP tool. The MCP server is in-process inside
   the adapter; CC calls out to it via stdio.
2. Pass `--mcp-config` pointing at the generated config and
   `--permission-mode bypassPermissions` so the subprocess doesn't prompt.
3. On tool use, the in-process MCP server returns a deferred result that
   blocks until Cursor sends the `role: "tool"` reply in a subsequent
   chat-completions request. This is the hardest piece — it changes the
   fallback from one-shot-subprocess to streaming-subprocess-plus-pending-
   tool-calls. May need to keep Phase 2.3 as a multi-PR effort.

**Why defer:** Cursor's tool set (`edit_file`, `codebase_search`,
`run_terminal_cmd`, …) doesn't map to MCP conventions, so the adapter
would be synthesizing an MCP façade around arbitrary JSON Schema tools.
Non-trivial. Phase 2.1 + 2.2 already solve the token-bill question; 2.3
only unlocks structural parity with CC's native tool handling.

## Files touched summary (Phase 2.1 + 2.2)

| File                                             | Change                                                   |
| ------------------------------------------------ | -------------------------------------------------------- |
| `internal/adapter/fallback/spawn.go`             | `--input-format stream-json`, `--session-id`, stdin pipe |
| `internal/adapter/fallback/types.go`             | `Message` gains structured content; `Request` gains `SessionID` |
| `internal/adapter/fallback_handler.go`           | `buildFallbackMessages` keeps structure; derive session ID |
| New: `internal/adapter/fallback/input_stream.go` | NDJSON writer for stream-json input                      |
| New: `internal/adapter/fallback/session_id.go`   | Deterministic session-id derivation                      |
| `internal/adapter/fallback/fallback_test.go`     | Fixture subprocess verifies NDJSON stdin shape + session reuse |

## Verification (Phase 2)

1. **Unit:** `go test ./internal/adapter/fallback/...` — fixture claude
   binary asserts:
   - Receives `--input-format stream-json` and `--session-id <uuid>`
   - Reads NDJSON from stdin, not positional arg
   - Same session id across two back-to-back `Collect` calls in the same
     conversation
   - Different session id across different conversations
2. **Cache efficacy:** run a 10-turn Cursor session routed exclusively
   through fallback (flip `[adapter.fallback].trigger` to `"always"` for
   the test run). Compare:

   ```
   jq -r 'select(.msg=="adapter.chat.completed" and .backend=="fallback")
     | "\(.tokens_in)\t\(.cache_read_tokens)"' \
     $XDG_STATE_HOME/clyde/clyde.jsonl
   ```

   Expect the `cache_read_tokens / (tokens_in + cache_read_tokens)` ratio
   to climb past 0.5 by turn 3 and stabilize above 0.7 by turn 6 — same
   targets as OAuth backend in Phase 1.
3. **Transcript reuse:** inspect
   `<ScratchDir>/.claude/projects/<cwd-encoded>/<session-id>.jsonl` after
   a multi-turn session — it should contain all turns, appended across
   invocations (proves `--session-id` actually re-uses the transcript).
4. **Regression:** existing `fallback_test.go` + `refactor_regression_test.go`
   must still pass; `make slog-audit` clean.

---

# Phase 2 (revised during execution): Narrowed to what delivers real value

## Discovery during implementation

On closer reading of `src/cli/print.ts:2816` and `src/cli/structuredIO.ts:135-213`,
`claude -p --input-format stream-json` reads NDJSON from stdin and treats each
`{type:"user",…}` line as a **new conversational turn that triggers its own
reply**. It is NOT a mechanism for feeding prior conversation history. Multi-turn
context only flows into a `-p` subprocess via:

1. `--resume <session-id>` with a pre-existing transcript JSONL file at
   `~/.claude/projects/<cwd-encoded>/<session-id>.jsonl`, OR
2. Flattening the history into a single user message (the current positional-arg
   approach).

This invalidates the original Phase 2.1 promise that stream-json stdin would
yield cache wins — stream-json stdin alone moves plumbing but not tokens.
Writing transcript JSONL ourselves is possible but fragile: the schema is
CC-internal, version-pinned, and part of `sessionStorage.ts` / `compact.ts` /
`sessionMemoryCompact.ts` — a moving target we don't want to shadow.

Meanwhile, Anthropic's prompt cache is **content-hash keyed**, which means the
current flattened positional-arg approach already produces cache hits when the
leading bytes match across turns. Phase 1's logging (shipped) is sufficient to
confirm this empirically.

## Narrowed ship: deterministic `--session-id` + cache-usage parity logs

Two surgical changes that deliver real value without risky scope creep.

### Change A — Deterministic `--session-id`

**Files:**

- `internal/adapter/fallback/types.go` — add `SessionID string` to `Request`.
- `internal/adapter/fallback/spawn.go` — pass `--session-id <uuid>` when non-empty.
- `internal/adapter/fallback_handler.go` — derive UUID from
  `(first user-message text + model alias)`, formatted as UUIDv4 via
  `internal/util.GenerateUUID` semantics (hash to 16 bytes, set variant/version
  bits).
- `internal/adapter/fallback/fallback_test.go` — assert flag is passed and UUID
  is stable across two invocations that share the first user message.

Why this helps:
- Same conversation → same transcript file → CC's internal message
  construction is stable byte-for-byte → content-hash prefix cache hits more
  reliably on turn 2+.
- Foundation for future transcript-writing work; adding `--resume` support
  later becomes a one-line change.
- Zero behavior change for callers — `SessionID` is optional and the flag is
  only emitted when non-empty.

### Change B — `adapter.cache.usage` slog parity for fallback

Phase 1 emits `adapter.cache.usage` with `hit_ratio` on the OAuth backend only.
Mirror it on the fallback backend so operators have one query to check either
path.

**Files:**

- `internal/adapter/fallback_handler.go` — call `s.logCacheUsage(...)` after
  both the collect and stream paths, same signature as the OAuth sites. The
  helper is already generalized against `anthropic.Usage` — need a tiny adapter
  that constructs an `anthropic.Usage` from the fallback's `Usage` shape, or
  promote `logCacheUsage` to take the OpenAI `Usage` directly.

### What I am dropping from Phase 2 vs. the approved plan

- **Stream-json stdin input.** Verified during implementation to not be a cache
  mechanism; ships zero real savings for our multi-turn pass-through model.
  Recorded as a dead end; does not block future work.
- **Native tools via MCP.** Deferred as originally planned; not in scope for
  this ship.

## Verification (revised)

1. **Unit:** `go test ./internal/adapter/fallback/...` — new test asserts
   `--session-id <uuid>` present in argv and stable across invocations sharing
   the first user message.
2. **Live Cursor session:** point Cursor at the adapter with fallback triggered,
   run a 10-turn session, tail the JSONL log for both backends:

   ```
   jq -c 'select(.msg == "adapter.cache.usage")
     | {backend, hit_ratio, cache_read_tokens, input_tokens}' \
     $XDG_STATE_HOME/clyde/clyde.jsonl
   ```

   Expect the fallback backend's `hit_ratio` to climb past 0.3 by turn 3. If it
   stays near zero, the content-hash approach isn't working and transcript
   writing becomes the next lever.
3. **Regression:** `go test ./internal/adapter/...` passes except the
   pre-existing `TestVersionFromUserAgent` (REDACTED-UA placeholder, unrelated).
   `make slog-audit` clean.

---

# Phase 3: Transcript JSONL writing for real multi-turn continuity

## Context

Phase 2 shipped deterministic `--session-id` but still flattens the whole
OpenAI history into the positional-arg prompt on every `claude -p` invocation.
Anthropic's prompt cache is content-hash keyed so the leading bytes of the
flattened string can hit cache, but (a) any small change early in history
invalidates everything, and (b) resending 30k-token histories round-trips that
entire prefix through the CLI → CC's API call every turn.

If we synthesize a Claude Code transcript JSONL mirroring the conversation
and pass `--resume <uuid>` instead of `--session-id <uuid>`, we send only the
new user message as the prompt. CC loads history from the transcript; cache
hits become reliable and per-turn payload shrinks from 30k tokens to 2k.

## Research findings (recorded so future work doesn't re-derive)

- **Transcript path:** `~/.claude/projects/<sanitizePath(cwd)>/<session-id>.jsonl`.
  `sanitizePath` replaces `[^a-zA-Z0-9]` with `-` (source:
  `src/utils/sessionStoragePortable.ts:311-319`). Root is `$CLAUDE_CONFIG_HOME`
  or `~/.claude` (`src/utils/sessionStorage.ts:202-204`).
- **Parser is permissive** (`src/utils/json.ts:146-150` `parseJSONL` try/catch
  skips malformed lines). Missing non-critical fields don't crash the resume.
- **`--resume <uuid>` is non-TTY under `-p`** (confirmed: `main.tsx:803`
  `isNonInteractive = hasPrintFlag || …`, and `print.ts:5029-5041` validates
  `--resume` specifically for the `-p` path). Safe to swap in.
- **`--resume` and `--session-id` shouldn't be combined** on the same call:
  `--session-id` creates/fresh; `--resume` loads existing. Turn 1 uses
  `--session-id`, turn 2+ uses `--resume` and drops `--session-id`.
- **`--resume` requires the file to exist**; `--session-id` starts fresh and
  writes its own transcript. So: first invocation of a conversation still
  uses `--session-id`, and we only start writing our synthetic transcript
  on turn 2 (the cost amortizes across the rest of the session).

## Minimum-viable line schema

```jsonl
{"type":"user","uuid":"<v4>","parentUuid":null,"sessionId":"<v4>",
 "message":{"role":"user","content":"hi"},"cwd":"<scratch-dir>",
 "userType":"user","version":"1.0.0","isSidechain":false,"timestamp":"<iso>"}
{"type":"assistant","uuid":"<v4>","parentUuid":"<prev>","sessionId":"<v4>",
 "message":{"role":"assistant","content":[{"type":"text","text":"reply"}],
 "model":"claude-<...>","stop_reason":"end_turn",
 "usage":{"input_tokens":10,"output_tokens":5}},
 "cwd":"<scratch-dir>","userType":"user","version":"1.0.0",
 "isSidechain":false,"timestamp":"<iso>"}
```

`parentUuid` chain is the only non-negotiable structural requirement; the
loader walks it from `null` forward to reconstruct linear history.

## Files

| New / modified                                           | Purpose                                                    |
| -------------------------------------------------------- | ---------------------------------------------------------- |
| New: `internal/adapter/fallback/transcript.go`           | `SynthesizeTranscript(msgs, sessionID, cwd) []Line` + `WriteTranscript(path, lines)` |
| New: `internal/adapter/fallback/transcript_path.go`      | `TranscriptPath(projectsRoot, cwd, sessionID) string` replicating `sanitizePath` |
| `internal/adapter/fallback/types.go`                     | `Request` gains `Resume bool` + `ProjectsDir string` (test override) |
| `internal/adapter/fallback/spawn.go`                     | When `Resume`: emit `--resume <uuid>` and drop `--session-id`; send only the latest user message as positional arg |
| `internal/adapter/fallback_handler.go`                   | Write transcript from `msgs[:-1]` before `Collect/Stream`; flip `Resume=true` when the file exists on disk |
| New: `internal/adapter/fallback/transcript_test.go`      | Round-trip tests: synthesize a 4-turn conversation, verify parentUuid chain + file layout |

## Edge cases

- **First turn:** no transcript yet → keep today's Phase-2 path
  (`--session-id <uuid>` + flattened prompt). Cheaper and safer. Start
  writing transcripts on turn 2.
- **History divergence** (user edits earlier message in Cursor): detect via
  in-memory UUID cache keyed by `sessionID`. On mismatch, wipe the transcript
  file and reset to `--session-id` fresh.
- **Pruning:** transcripts accumulate in scratch-dir's projects folder. Add
  a 24h-TTL sweep via the daemon's existing `internal/prune` loops.
- **Tool-use id translation:** synthesizing an assistant-turn with prior
  tool_use blocks requires real Anthropic tool_use_id format, not clyde's
  `call_<reqid>_<idx>` synth. Map/regenerate at synthesis time.

## Risk

- **Schema is version-pinned to CC.** Mitigation: emit a warn-level slog
  event on daemon start if installed CC version is outside a tested range
  (read from `claude --version`).
- **Permissive parsing cuts both ways:** if we forget a field CC silently
  expects, the line is dropped and resume loses a turn. Add an integration
  test that spawns the real `claude -p --resume` against a synthesized
  transcript and asserts history is intact.

## Estimated savings (on top of Phase 2)

15-turn fallback session: current cache hit ratio ~30–50% (prefix-hash
luck). With transcript writing: stable ~70–85% hit ratio, plus per-turn
payload drops from ~30k tokens to ~2k. Roughly **50–70% further
reduction** in input bill over Phase 2's baseline.

## Ship checklist

1. Transcript write + read round-trip unit tests pass.
2. Integration test: spawn real `claude -p --resume <uuid>` against a
   synthesized transcript, assert the subprocess sees the full history.
3. Live Cursor session through fallback, `adapter.cache.usage` `hit_ratio >
   0.7` by turn 3 (mirrors OAuth backend targets).
4. No regression in existing `fallback_test.go`; `make slog-audit` clean.

---

# Phase 4: Native MCP tools via a UDS-bridged relay

## Context

`fallback/tools.go:27-81` prepends 5–10k tokens of JSON-Schema tool definitions
plus "respond with `{\"tool_calls\":[…]}`" instructions into
`--append-system-prompt` on every request. Cursor's tool schemas ride along
uncached on every turn (tool-schema caching was already marked in Phase 1 for
the OAuth backend; fallback doesn't use those markers). Eliminating the
preamble and registering tools natively via MCP saves tokens AND gives us
real `tool_use`/`tool_result` blocks instead of JSON-envelope parsing.

## The architectural tension (confirmed)

MCP tool calls are **synchronous within one `claude -p` invocation**. Cursor's
OpenAI flow is **asynchronous across chat.completions requests**. No way to
pause one `claude -p` subprocess until the next HTTP request arrives.

## Resolution: kill-and-resume

1. Adapter spawns in-process Go MCP server on a per-request UDS.
2. `--mcp-config` passed to `claude -p` points at `clyde mcp-relay <socket>`,
   a small stdio-to-UDS bridge subcommand.
3. MCP server advertises Cursor's tools via `tools/list`.
4. On `tools/call`, the handler:
   a. Writes the tool-call to a Go channel in the adapter.
   b. After a short grace period (~500ms for any in-flight text to flush),
      SIGTERMs the `claude -p` subprocess.
   c. Returns an MCP error to the (dying) subprocess.
5. Adapter returns `{tool_calls: [...]}` to Cursor in the chat.completions
   response.
6. Next chat.completions arrives with `role: "tool"` content. Adapter appends
   tool_use + tool_result to the transcript (Phase 3 infrastructure), then
   re-spawns `claude -p --resume <uuid>` with no new user prompt.
7. CC reads the transcript, sees the tool_result, continues.

Phase 3 is a prerequisite: without transcript writing there's no way to
persist the tool_use/tool_result pair between the two subprocess
invocations.

## Files

| New / modified                                           | Purpose                                                 |
| -------------------------------------------------------- | ------------------------------------------------------- |
| New: `internal/adapter/fallback/mcp_server.go`           | `ServeTools(ctx, socket, tools []Tool) (<-chan ToolCall, error)` using `github.com/mark3labs/mcp-go v0.47.1` (already in `go.mod`) |
| New: `internal/adapter/fallback/mcp_config.go`           | Generate per-request mcp-config JSON pointing at the relay binary + socket |
| New: `cmd/mcp_relay.go`                                  | `clyde mcp-relay <socket>` — ~50 lines of stdio↔UDS byte copying |
| `internal/adapter/fallback/spawn.go`                     | Add `--mcp-config <path>` and `--permission-mode bypassPermissions` to argv when tools are present |
| `internal/adapter/fallback_handler.go`                   | Tool-call channel listener; on signal, kill subprocess + return `tool_calls` to Cursor |
| `internal/adapter/fallback/tools.go`                     | When MCP is active, skip `renderToolsPreamble` (save 5–10k tokens/turn) |
| New: `internal/adapter/fallback/mcp_integration_test.go` | Fake claude binary invokes `tools/call`; assert adapter returns OpenAI-shaped tool_calls |

## Why the relay subcommand

MCP's stdio transport spawns a subprocess per server — no way to pass FDs to
self without exec plumbing below Go stdlib. The relay is ~50 lines of byte
shuffling; all MCP-JSON-RPC logic stays in the parent daemon. Avoids
rewriting MCP protocol twice and avoids the daemon fork-with-socketpair
gymnastics.

## Reuse

- `github.com/mark3labs/mcp-go v0.47.1` — already in `go.mod`.
- Clyde already ships a `clyde mcp` server for session search
  (`internal/cli/mcp/mcp.go`). Not directly reusable (different tool shape)
  but the server-setup idioms transfer.

## Risk / honest caveats

- **Subprocess kill is aggressive.** Claude mid-thinking-block when it
  dispatches a tool loses the in-flight thinking. Not correctness, but
  Cursor sees a hard stop. Mitigation: 500ms grace before SIGTERM.
- **Stateful adapter.** First piece of cross-request state. Keep minimal:
  `map[sessionID]pendingToolCall` keyed by session-id, TTL-cleaned.
- **Permission surface bypassed.** `--permission-mode bypassPermissions`
  overrides operator-tightened permissions. Document in README + emit a
  daemon-start warn slog event when fallback tools are active.

## Estimated savings (on top of Phase 3)

- Per-turn tool preamble: 5–10k tokens saved on every request.
- Cursor tools become actual cacheable tool schemas (marked ephemeral like
  Phase 1 does for OAuth).
- Combined Phase 3 + Phase 4: **75–90% total input-bill reduction** vs.
  pre-caching status quo. Matches OAuth-backend performance.

## What we are NOT doing in Phase 4

- **Tool-permission prompts.** Bypass with `--permission-mode`. Adding
  real permission plumbing would triple the scope and isn't a token
  concern.
- **MCP-over-SSE or HTTP transport.** Stdio is the simplest and most
  commonly-tested path.
- **Sharing MCP server with clyde's own `clyde mcp` command.** Different
  tool set; different permission model; keep them separate.

## Ship order

Phase 3 first (low-risk, measurable via logs, unlocks real cache wins).
Phase 4 only after Phase 3 is stable in production — Phase 4 depends on
Phase 3's transcript writer for its tool-call continuation path.
