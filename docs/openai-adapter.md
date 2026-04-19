# OpenAI compatible adapter

The clyde daemon ships with an optional HTTP surface that speaks
the OpenAI Chat Completions v1 API. It lets any OpenAI compatible
client (Cursor, Aider, Cline, Open WebUI, LangChain, custom agents)
drive the Claude Max subscription through `claude -p` instead of
billing to a separate API key.

The adapter runs inside the same process as the gRPC daemon. One
launchd entry boots everything. There is no second service to manage.

# Tool calling, vision, audio, and logprobs

The adapter will implement the full OpenAI `chat.completions` tool surface across all three backends. Earlier versions returned 400 on any request carrying tools or functions; that hard rejection will be gone. Request and message wire shapes follow `ChatRequest`, `Tool`, `ToolCall`, `ContentPart`, and `ImageURLPart` in `internal/adapter/openai.go`. Per-backend behavior is described below.

## Tool calling

Translation between OpenAI tools and Anthropic tools happens in `internal/adapter/tooltrans`. Both directions will be covered:

| OpenAI request field                                  | Anthropic equivalent                         | Notes                                    |
| ----------------------------------------------------- | -------------------------------------------- | ---------------------------------------- |
| `tools[].function.name`                               | `tools[].name`                               | verbatim                                 |
| `tools[].function.description`                        | `tools[].description`                        | verbatim                                 |
| `tools[].function.parameters`                         | `tools[].input_schema`                       | JSON Schema passed through               |
| `tool_choice` `"auto"`                                | `tool_choice` `{type:"auto"}`                | default                                  |
| `tool_choice` `"none"`                                | `tool_choice` `{type:"none"}`                | model never calls a tool                 |
| `tool_choice` `"required"`                            | `tool_choice` `{type:"any"}`                 | model must call some tool                |
| `tool_choice` `{type:"function",function:{name:"x"}}` | `tool_choice` `{type:"tool",name:"x"}`       | named tool                               |
| `parallel_tool_calls=false`                           | `tool_choice.disable_parallel_tool_use=true` | applied to whatever `tool_choice` is set |
| `functions[]` (legacy)                                | `tools[]`                                    | legacy field is translated identically   |

Assistant tool calls will be surfaced on the response in the standard OpenAI shape:

```json
{
  "choices": [
    {
      "message": {
        "role": "assistant",
        "content": null,
        "tool_calls": [
          {
            "id": "toolu_...",
            "type": "function",
            "function": {
              "name": "get_weather",
              "arguments": "{\"loc\":\"NYC\"}"
            }
          }
        ]
      },
      "finish_reason": "tool_calls"
    }
  ]
}
```

The Anthropic block id (`toolu_...`) will be preserved as the OpenAI `tool_call.id` so the next-turn `role:"tool"` message round-trips cleanly. Arguments will arrive as a JSON-encoded string per the OpenAI spec, never as a JSON object.

On the next turn, `role:"tool"` messages will be translated into Anthropic user messages carrying a `tool_result` block:

```json
{ "role": "tool", "tool_call_id": "toolu_abc", "content": "42" }
```

becomes

```json
{
  "role": "user",
  "content": [
    { "type": "tool_result", "tool_use_id": "toolu_abc", "content": "42" }
  ]
}
```

### Streaming

Anthropic streams will emit `content_block_start`, `content_block_delta` (`text_delta` or `input_json_delta`), and `content_block_stop`. The translator in `internal/adapter/tooltrans/stream.go` will convert these into OpenAI delta chunks:

| Anthropic SSE                            | OpenAI SSE delta                                                                 |
| ---------------------------------------- | -------------------------------------------------------------------------------- |
| `content_block_start` type=text          | (no chunk; text deltas follow)                                                   |
| `content_block_delta` `text_delta`       | `delta.content`                                                                  |
| `content_block_start` type=tool_use      | `delta.tool_calls=[{index, id, type:"function", function:{name, arguments:""}}]` |
| `content_block_delta` `input_json_delta` | `delta.tool_calls=[{index, function:{arguments:partial_json}}]`                  |
| `content_block_stop`                     | (no chunk)                                                                       |
| `message_delta` `stop_reason`            | (captured for `finish_reason` chunk)                                             |
| `message_stop`                           | `finish_reason` chunk                                                            |

Tool call indices will be stable: the first tool block gets index 0, the second gets index 1, etc., matching the OpenAI streaming spec where each `tool_call.index` identifies which call the arguments delta belongs to.

### claude -p fallback

The local CLI does not natively accept tool definitions. The fallback driver in `internal/adapter/fallback` will render a strict-format preamble into the system prompt:

```
You have access to the following tools:

{
  "name": "get_weather",
  "description": "...",
  "parameters": { ...JSON Schema... }
}

When you decide to call a tool, your ENTIRE response MUST be a single JSON object on one line of the exact form:
{"tool_calls":[{"name":"<tool_name>","arguments":{...}}]}
```

The driver will scan the assistant output for a trailing JSON envelope. When found, it will strip that line from the surfaced text, synthesize OpenAI `tool_call` ids (`"call_<random>"`), and set `finish_reason="tool_calls"`. When absent, the response will surface as plain text. `tool_choice` values will map as follows on this backend:

| tool_choice                             | preamble behavior                             |
| --------------------------------------- | --------------------------------------------- |
| `"auto"` / unset                        | all tools listed, no constraint               |
| `"none"`                                | tools NOT listed (avoid tempting the model)   |
| `"required"`                            | "You MUST call exactly one tool"              |
| `{type:"function",function:{name:"x"}}` | only x's definition listed, "You MUST call x" |

Streaming with tools enabled will be buffered: the driver will collect the entire stream before parsing the envelope, then emit OpenAI-shaped `tool_call` delta chunks. True per-token streaming of partial tool arguments will not be safe to attempt on this backend because the envelope is plaintext and a partial parse can reorder fields.

### Shunt passthrough

For `[adapter.shunts.*]`, `tools`, `tool_choice`, `parallel_tool_calls`, and tool messages will be forwarded verbatim. Earlier versions stripped tool fields on the way out; that strip will be gone. `response_format` `json_schema` will still be stripped on shunts that do not support it (LM Studio, Ollama, etc.) and replaced with a JSON-mode system prompt + post-validate.

## Vision (image input)

OpenAI `image_url` content parts will be accepted on the Anthropic backend:

| OpenAI part                                                      | Anthropic block                                                           |
| ---------------------------------------------------------------- | ------------------------------------------------------------------------- |
| `{type:"image_url",image_url:{url:"data:image/png;base64,..."}}` | `{type:"image",source:{type:"base64",media_type:"image/png",data:"..."}}` |
| `{type:"image_url",image_url:{url:"https://x/y.png"}}`           | `{type:"image",source:{type:"url",url:"https://x/y.png"}}`                |

The `detail` field on `image_url` will be dropped (Anthropic auto-tunes resolution).

Per-family vision support will be declared in `[adapter.families.<slug>].supports_vision` (no default; `NewRegistry` will reject an unset value). Requesting a vision-incapable model with image content will return 400 with code `"unsupported_content"`.

The `claude -p` fallback will NOT support image input. Image content parts on a request routed to the fallback will return 400 with code `"fallback_no_vision"`.

Shunts will pass `image_url` through verbatim. The shunted upstream will decide whether to honor it.

## Audio

Audio input (`input_audio` content parts) will not be supported on any backend at first. The adapter will return 400 with code `"audio_unsupported"` the moment it sees an `input_audio` part, before any backend dispatch. Audio output (`modalities:["audio"]`) will also be rejected.

## Logprobs

OpenAI `logprobs` / `top_logprobs` handling will be configured per-backend under `[adapter.logprobs]` (no compiled-in defaults; `NewRegistry` will require every backend key when the request carries logprobs):

```toml
[adapter.logprobs]
anthropic = "reject"   # "reject" -> 400, "drop" -> silently strip
fallback  = "reject"
```

Shunts will always pass through verbatim regardless of the stanza. The Anthropic `/v1/messages` API does not emit logprobs; `"drop"` is the polite alternative to `"reject"` for clients that send the field defensively but do not need the response.

## Per-family capability flags

Two new keys on `[adapter.families.<slug>]`:

```toml
[adapter.families.opus-4-7]
# ...existing keys...
supports_tools  = true
supports_vision = true
```

Both must be set explicitly. `NewRegistry` will return an error when either is missing on any family. The adapter will pre-flight every request against the resolved model's capabilities and return 400 before dispatching when the request asks for an unsupported feature.

## Pre-flight summary

Order of checks in `handleChat` for any `chat.completions` request:

1. messages required (400 `invalid_request`).
2. Resolve alias -> `ResolvedModel` (400 `unknown_model`).
3. Audio content parts present anywhere -> 400 `audio_unsupported`.
4. Image content parts present + `ResolvedModel.SupportsVision == false` -> 400 `unsupported_content`.
5. `tools`/`tool_choice` present + `ResolvedModel.SupportsTools == false` -> 400 `unsupported_content`.
6. logprobs requested + backend `Logprobs == "reject"` -> 400 `unsupported_param`.
7. Backend dispatch.

## Enable

The adapter has no compiled-in model registry or impersonation
defaults. Bootstrap by copying the on-disk reference example
(`clyde.example.toml` at the repo root, family matrix empirically
validated 2026-04-18) into your config and restarting the daemon:

```bash
cat clyde.example.toml >> ~/.config/clyde/config.toml
$EDITOR ~/.config/clyde/config.toml   # tweak if desired
launchctl kickstart -k gui/$UID/io.goodkind.clyde
```

The reference stanza includes:

- `[adapter]` with `enabled`, `direct_oauth`, and `default_model`.
- `[adapter.impersonation]` -- the three Claude Code identity
  signals (`beta_header`, `user_agent`, `system_prompt_prefix`) the
  adapter mirrors on every `/v1/messages` call. Empty fields fail
  registry construction; the daemon refuses to start the listener.
- `[adapter.families.<slug>]` for `opus-4-7`, `sonnet-4-6`, and
  `haiku-4-5`, each declaring `model`, `efforts`, `thinking_modes`,
  `max_output_tokens`, and `contexts`. The registry expands the
  cross product into individual aliases at load time.

The listener binds `127.0.0.1:11434` by default.

Point any client at it:

```
OPENAI_BASE_URL=http://localhost:11434/v1
OPENAI_API_KEY=none
```

## Generated alias schema

The registry has no static alias list. It expands the cross product
of efforts × thinking modes × contexts declared in
`[adapter.families.<slug>]` into individual aliases at load time.

Schema:

```
clyde-<family>[-<effort>][-<ctx-suffix>][-thinking-<mode>]
```

`thinking-default` (the implicit baseline) is omitted. Effort is
omitted for families that declare an empty `efforts` list (haiku).

Examples produced by the reference toml:

| Alias                                      | Backend model               | Context   | Effort | Thinking |
| ------------------------------------------ | --------------------------- | --------- | ------ | -------- |
| `clyde-opus-4-7-high-1m`                   | `claude-opus-4-7[1m]`       | 1,000,000 | high   | default  |
| `clyde-opus-4-7-high-1m-thinking-adaptive` | `claude-opus-4-7[1m]`       | 1,000,000 | high   | adaptive |
| `clyde-opus-4-7-max`                       | `claude-opus-4-7`           | 200,000   | max    | default  |
| `clyde-sonnet-4-6-medium`                  | `claude-sonnet-4-6`         | 200,000   | medium | default  |
| `clyde-sonnet-4-6-low-thinking-enabled`    | `claude-sonnet-4-6`         | 200,000   | low    | enabled  |
| `clyde-haiku-4-5`                          | `claude-haiku-4-5-20251001` | 200,000   | none   | default  |
| `clyde-haiku-4-5-thinking-enabled`         | `claude-haiku-4-5-20251001` | 200,000   | none   | enabled  |
| `gpt-4o`                                   | shunt                       | n/a       | n/a    | n/a      |

Run `curl http://localhost:11434/v1/models` against the live adapter
for the full enumeration; with the reference matrix that is opus
(2 contexts × 4 efforts × 4 thinking = 32) plus sonnet (1 × 4 × 4 = 16) plus haiku (1 × 0 × 3 = 3) for a total of 51.

Set `reasoning_effort` on the OpenAI request to override the alias
bound effort. Unsupported effort values 400 with the family's
allowed list.

## Add custom models

Layer overrides and new aliases through the standard config system:

```toml
[adapter.models.fast-haiku]
backend = "claude"
model = "claude-haiku-4-5-20251001"
context = 200000
efforts = ["low"]

[adapter.models.my-gpt]
backend = "shunt"
shunt = "openai"
```

## Shunt a blunt gpt-4o

Configure an upstream endpoint so the `gpt-4o` alias (or any custom
alias) forwards to a real OpenAI compatible server:

```toml
[adapter.shunts.openai]
base_url = "https://api.openai.com/v1"
api_key_env = "OPENAI_API_KEY"
# model = "gpt-4o"  # optional rename on the way out
```

Requests routed to a shunt are proxied verbatim after the model name
rewrite. No claude subprocess is involved.

## Fallback layer

The adapter ships a third backend that drives the local `claude`
CLI in `-p --output-format stream-json` mode. It is fully toml
configured, has no compiled-in defaults, and is disabled out of
the box. The repo-root `clyde.example.toml` ships a complete
`[adapter.fallback]` stanza with every key present and
`enabled = false`. Flip `enabled = true` and pick a trigger to
opt in.

### Triggers

`[adapter.fallback].trigger` selects when the dispatcher fires:

- `explicit` -- only when an alias resolves to
  `backend = "fallback"` (e.g. a user-defined
  `[adapter.models.<name>]` entry).
- `on_oauth_failure` -- only when a direct-OAuth attempt errors
  before any byte has been written to the wire. Streaming requests
  can only escalate before the first delta flushes.
- `both` -- explicit aliases plus oauth-failure escalation.

### Forward to a shunt instead

`[adapter.fallback.forward_to_shunt]` opts the trigger path into
a configured shunt before (in lieu of) `claude -p`. Useful when
you want spillover to a paid OpenAI account when the Claude.ai
bucket is exhausted, without launching the heavier subprocess.
The named shunt must exist in `[adapter.shunts.<name>]` or the
registry refuses to start.

### CLI alias mapping

`claude -p --model` accepts short family names (`opus`, `sonnet`,
`haiku`) that don't match the wire-level Anthropic ids the OAuth
path sends. `[adapter.fallback.cli_aliases]` is the family-slug to
CLI-name mapping. Every slug listed in `allowed_families` must
have an entry; missing entries fail registry construction.

### Failure escalation

When both the OAuth attempt and the fallback attempt fail,
`[adapter.fallback].failure_escalation` picks which surfaces:

- `fallback_error` -- the client sees the `claude -p` error
  (default in the reference toml; surfaces the most recent
  attempt).
- `oauth_error` -- the client sees the original OAuth error
  (useful when the fallback is "best effort" and you want
  upstream-quota errors to remain visible).

### Silently dropped fields

`claude -p` does not expose flags for `reasoning_effort` or
extended thinking. With `drop_unsupported = true` the adapter
ignores those fields and emits an `adapter.fallback.dropped_field`
debug log per occurrence. A future revision will inject the
equivalent settings via per-session settings.json so these
features round-trip cleanly; until then the fallback path is a
"capability subset" of the OAuth path.

### Concurrency

The fallback path holds its own semaphore (`max_concurrent`)
sized independently of the OAuth path's `[adapter].max_concurrent`.
A subprocess is heavier than an HTTP call, so a smaller cap is
usually the right call.

### Recursion guard

`suppress_hook_env = true` sets `CLYDE_DISABLE_DAEMON=1` and
`CLYDE_SUPPRESS_HOOKS=1` on the spawned subprocess. Without
these, the spawned `claude` triggers `clyde hook sessionstart`,
which dials back into the daemon, which can spawn another
`claude -p` if the labeler ever returns. Leave it on.

## Verified compliance

The adapter has been physically battle tested against a running daemon
on macOS with `claude-haiku-4-5-20251001` as the upstream model.

### Endpoints exercised

```
GET  /healthz                  → 200 {"status":"ok"}
GET  /v1/models                → 200 11 entries (built-ins + gpt-4o shunt)
POST /v1/chat/completions      → non streaming, real claude reply
POST /v1/chat/completions      → streaming with stream_options.include_usage
POST /v1/chat/completions      → tools field rejected with 400 unsupported
POST /v1/chat/completions      → unknown model falls back to default_model
POST /v1/completions           → legacy text completion wrapper
```

### Shape compliance

Every documented top level field on the OpenAI Chat Completions request
is parsed without rejection: `temperature`, `top_p`, `max_tokens`,
`max_completion_tokens`, `stop`, `presence_penalty`, `frequency_penalty`,
`logit_bias`, `logprobs`, `top_logprobs`, `seed`, `response_format`,
`audio`, `modalities`, `parallel_tool_calls`, `store`, `metadata`, `n`,
`user`, `stream`, `stream_options`, `reasoning_effort`, `tools`,
`tool_choice`, `function_call`, `functions`.

The non streaming response object includes `id`, `object`, `created`,
`model`, `choices[].index`, `choices[].message.role`,
`choices[].message.content`, `choices[].finish_reason`, `usage` with
`prompt_tokens` / `completion_tokens` / `total_tokens`, and
`system_fingerprint`. The streaming chunk object follows the
`chat.completion.chunk` shape with delta role on the first chunk only,
a final chunk with `finish_reason: "stop"` and empty delta, an
optional usage chunk when `stream_options.include_usage` is true, and
the literal `data: [DONE]` terminator.

### Concurrency and isolation

Six simultaneous requests against a `max_concurrent = 4` adapter
finished in 9 seconds. The semaphore serialised the overflow so the
upstream never saw more than four in flight. The discovery scanner
skips transcripts written from the adapter scratch directory at
`~/Library/Caches/clyde/adapter-scratch` so adapter traffic never
pollutes the clyde session list.

## Not in version one

Embeddings return a 400 with an explicit message. Audio input and
audio output return 400 across all backends. Tool calling, function
calling, and image input are now supported as described in the tool
calling, vision, audio, and logprobs section above.

## OAuth bucket impersonation drift (429 root cause)

The Anthropic `/v1/messages` OAuth path applies different throttling
buckets based on the impersonation signals on each request. If clyde
sends an impersonation set that does not match what the current `claude`
CLI sends, the request lands in a smaller (or zero) bucket and returns
HTTP 429 with body `{"type":"error","error":{"type":"rate_limit_error","message":"Error"}}`
for prompts that the official CLI handles without issue.

### Reproduction (verified 2026-04-19, concurrent run)

Two requests fired in the same shell pipeline against the same OAuth
token and the same `claude-opus-4-7` upstream model:

| Path                                                       | Status                       | Duration | Body bytes  |
| ---------------------------------------------------------- | ---------------------------- | -------- | ----------- |
| Clyde adapter `/v1/chat/completions` (alias `clyde-opus-4-7-medium-1m`) | `502` wrapping upstream `429 rate_limit_error` | 318 ms   | 68,670      |
| `claude -p --model claude-opus-4-7` via mitm proxy         | `200` with assistant content `"ok"`            | 3,563 ms | 238,790     |

Same instant, same token, same upstream API: **only the request headers
differed**, so Anthropic routed the two requests into different
throttling buckets.

### Testbed instrumentation

Two pieces work together so the diff can be re-derived on demand
without a wire sniffer.

#### 1. `clyde-research/tools/anthropic-mitm`

Forward proxy for `REDACTED-UPSTREAM`. Reads `Authorization` (redacts
to length marker) and writes one ndjson line per request with both raw
and base64 copies of the request and response body
([clyde-research/tools/anthropic-mitm/main.go](../../../clyde-research/tools/anthropic-mitm/main.go)):

| Field                       | Notes                                                                              |
| --------------------------- | ---------------------------------------------------------------------------------- |
| `request_method`, `request_path`, `request_headers` | `Authorization` and `x-api-key` redacted to `<REDACTED len=N>`           |
| `request_body_bytes`        | full untruncated size                                                              |
| `request_body_raw`          | UTF-8 string body, capped at 1 MiB (was 32 KiB; bumped so 240 KiB CLI bodies fit)  |
| `request_body_b64`          | base64 of the same bytes; survives truncation without breaking outer JSON          |
| `request_body_truncated`    | `true` if either capped form was clipped                                           |
| `response_*` mirror set     | same triple plus `response_status_code`, `response_headers`                        |
| `started_at`, `duration_ms` | wall-clock                                                                         |

Invocation:

```bash
TMPMOD=$(mktemp -d) && cp /Users/agoodkind/Sites/clyde-research/tools/anthropic-mitm/main.go "$TMPMOD/" \
  && (cd "$TMPMOD" && go mod init anthmitm && go build -o /tmp/anthropic-mitm .)
/tmp/anthropic-mitm -out /tmp/anthropic-capture.ndjson -addr 127.0.0.1:19999 &
ANTHROPIC_BASE_URL=http://127.0.0.1:19999 \
  /Users/agoodkind/.cursor/extensions/anthropic.claude-code-2.1.114-darwin-arm64/resources/native-binary/claude \
  -p "say hi" --model claude-opus-4-7
kill %1
```

#### 2. Clyde slog event `anthropic.messages.request`

Emitted in [internal/adapter/anthropic/client.go](../internal/adapter/anthropic/client.go)
right before `c.http.Do(httpReq)`. Lands in `~/.local/state/clyde/clyde.jsonl`
when `[logging] level = "debug"`. Attribute set:

| Attribute     | Value                                                                |
| ------------- | -------------------------------------------------------------------- |
| `subcomponent`| `"anthropic"`                                                        |
| `model`       | wire-format Anthropic model id                                       |
| `url`         | `MessagesURL`                                                        |
| `body_bytes`  | full size                                                            |
| `headers`     | `map[string]string`, lowercased keys, `authorization` redacted to `Bearer <redacted len=N>`, `x-api-key`/`cookie`/`proxy-authorization` to `<redacted>` |
| `body`        | raw JSON request body, full                                          |
| `body_b64`    | base64 of the same bytes                                             |

The same `body`/`body_b64`/`body_bytes` triple is also populated on
`anthropic.ratelimit` and `anthropic.messages.upstream_error` events
([internal/adapter/anthropic/logging.go](../internal/adapter/anthropic/logging.go)).
The earlier behavior of truncating error bodies to 400 chars is gone;
the response body is now logged in full.

#### Diff script

[/tmp/diff_headers.py](/tmp/diff_headers.py) (regenerable) reads the
mitm ndjson and the clyde jsonl, decodes both bodies via base64, and
prints a `[= same | + clyde-only | - cli-only | ! differ]`-marked
header diff plus a sorted `anthropic-beta` set delta.

### Captured ground truth

Captured via [clyde-research/tools/anthropic-mitm](../../../clyde-research/tools/anthropic-mitm)
pointed at `claude -p` with `ANTHROPIC_BASE_URL=http://127.0.0.1:19999`.
The successful CLI request was:

```
POST /v1/messages
Anthropic-Beta:    REDACTED-CC-BETA,REDACTED-OAUTH-BETA,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05,advisor-tool-2026-03-01,effort-2025-11-24
Anthropic-Version: 2023-06-01
User-Agent:        REDACTED-UA
X-App:             cli
X-Claude-Code-Session-Id: <uuid>
X-Stainless-Lang:  js
X-Stainless-Package-Version: 0.81.0
X-Stainless-Os:    MacOS
X-Stainless-Arch:  arm64
X-Stainless-Runtime: node
X-Stainless-Runtime-Version: v24.3.0
X-Stainless-Retry-Count: 0
X-Stainless-Timeout: 600
Anthropic-Dangerous-Direct-Browser-Access: <present>
Content-Type:      application/json
Accept:            application/json
Accept-Encoding:   gzip, deflate, br, zstd
```

What clyde currently sends from
[internal/adapter/anthropic/client.go:109-114](../internal/adapter/anthropic/client.go):

```
Authorization:     Bearer <token>
anthropic-beta:    REDACTED-OAUTH-BETA,REDACTED-CC-BETA,effort-2025-11-24
anthropic-version: 2023-06-01
x-app:             cli
User-Agent:        REDACTED-UA
Content-Type:      application/json
```

### Diff (machine-verified, 2026-04-19)

Generated from a concurrent run by `diff_headers.py`. Markers:
`= same`, `+ clyde-only`, `- cli-only`, `! differ`.

```
[= same]   anthropic-version
[= same]   content-type
[= same]   user-agent              -- REDACTED-UA
[= same]   x-app                   -- cli
[! differ] anthropic-beta
            cli   : REDACTED-CC-BETA,REDACTED-OAUTH-BETA,interleaved-thinking-2025-05-14,
                    context-management-2025-06-27,prompt-caching-scope-2026-01-05,
                    advisor-tool-2026-03-01,effort-2025-11-24             (7 betas)
            clyde : REDACTED-OAUTH-BETA,REDACTED-CC-BETA,effort-2025-11-24 (3 betas)
[- cli-only] accept                                       -- application/json
[- cli-only] accept-encoding                              -- gzip, deflate, br, zstd
[- cli-only] anthropic-dangerous-direct-browser-access    -- true
[- cli-only] x-claude-code-session-id                     -- <per-process uuid>
[- cli-only] x-stainless-lang                             -- js
[- cli-only] x-stainless-package-version                  -- 0.81.0
[- cli-only] x-stainless-os                               -- MacOS
[- cli-only] x-stainless-arch                             -- arm64
[- cli-only] x-stainless-runtime                          -- node
[- cli-only] x-stainless-runtime-version                  -- v24.3.0
[- cli-only] x-stainless-retry-count                      -- 0
[- cli-only] x-stainless-timeout                          -- 600
```

Beta-set delta in cleaner form. Source of truth for each missing beta
is in the deobfuscated CLI source:

| Missing beta                       | Source of truth in CLI v2.1.114                                                                                                          |
| ---------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------- |
| `interleaved-thinking-2025-05-14`  | [src/utils/betas.ts:257-262](../../../clyde-research/claude-code-sourcemap-main-2-newer/restored-src/src/utils/betas.ts) gated on `modelSupportsISP(model)` (opus-4 / sonnet-4 / haiku-4) |
| `context-management-2025-06-27`    | [src/utils/betas.ts:307-312](../../../clyde-research/claude-code-sourcemap-main-2-newer/restored-src/src/utils/betas.ts) gated on `modelSupportsContextManagement(model)`                |
| `prompt-caching-scope-2026-01-05`  | [src/utils/betas.ts:354-357](../../../clyde-research/claude-code-sourcemap-main-2-newer/restored-src/src/utils/betas.ts) firstParty + experimental-betas-not-disabled                    |
| `advisor-tool-2026-03-01`          | [src/constants/betas.ts:31](../../../clyde-research/claude-code-sourcemap-main-2-newer/restored-src/src/constants/betas.ts) constant; emitted unconditionally in 2.1.114                 |

Additional missing headers:

| Header                                       | Source                                                                                                                          |
| -------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------- |
| `X-Claude-Code-Session-Id`                   | [src/services/api/client.ts:108](../../../clyde-research/claude-code-sourcemap-main-2-newer/restored-src/src/services/api/client.ts) (per-process UUID) |
| `X-Stainless-*` block                        | injected by `@anthropic-ai/sdk` v0.81.0; static for one CLI version                                                             |
| `Anthropic-Dangerous-Direct-Browser-Access`  | Anthropic SDK default for non-browser environments                                                                              |
| `Accept`, `Accept-Encoding`                  | Anthropic SDK fetch defaults                                                                                                    |

An earlier draft of this section claimed `effort-2025-11-24` was not in
the CLI request. That was wrong; the v2.1.114 capture above shows the
CLI does send it on `claude-opus-4-7`. Despite the deobfuscated comment
in [claude-code/06-named/deobfuscated.js:667257](../../../clyde-research/claude-code/06-named/deobfuscated.js)
labeling it deprecated and "GA on 4.6", the runtime still ships it on
4.7. Keep it on clyde's side.

### What this means

The clyde anthropic client uses a static `BetaHeader` string from
`[adapter.impersonation]` in `~/.config/clyde/config.toml`. Hardcoding
a single beta string cannot match the per-model, per-feature logic in
`getAllModelBetas(model)`. Two paths forward:

1. **Quick fix**: replace the configured `beta_header` value with the
   superset captured above. This will bring clyde into the same bucket
   as `claude -p` for opus-4-7-style requests but will drift again the
   next time the CLI updates.
2. **Right fix**: port `getAllModelBetas(model)` to Go in
   `internal/adapter/anthropic` so the beta set is computed per request
   from the resolved model id, mirroring the CLI's logic. Add the
   `X-Claude-Code-Session-Id` header (one UUID per daemon process is
   sufficient) and the `X-Stainless-*` block. The `Stainless` headers
   are observability metadata only and have no per-request variation
   inside one CLI version, so they can be a static block keyed off the
   CLI version we are impersonating.

### How to re-verify after a CLI update

The full reproduction is one shell pipeline once the proxy is built and
clyde is running with `[logging] level = "debug"`:

```bash
# 1. start the proxy (build instructions above under "Testbed instrumentation")
truncate -s 0 ~/.local/state/clyde/clyde.jsonl
rm -f /tmp/anthropic-capture.ndjson
/tmp/anthropic-mitm -out /tmp/anthropic-capture.ndjson -addr 127.0.0.1:19999 &

# 2. build a 60-100 KiB prompt that reliably trips the bucket
python3 -c 'print("Repeat back the word ok. " + "Lorem ipsum dolor sit amet, consectetur adipiscing elit. " * 600)' > /tmp/big-prompt.txt
python3 -c 'import json; print(json.dumps({"model":"clyde-opus-4-7-medium-1m","messages":[{"role":"system","content":"You are helpful."+" Lorem ipsum dolor sit amet, consectetur adipiscing elit."*600},{"role":"user","content":open("/tmp/big-prompt.txt").read()}],"max_tokens":64,"stream":False}))' > /tmp/big-req.json

# 3. fire both at once
TOKEN=$(awk -F'"' '/require_token/ {print $2}' ~/.config/clyde/config.toml)
CLAUDE_BIN=/Users/agoodkind/.cursor/extensions/anthropic.claude-code-2.1.114-darwin-arm64/resources/native-binary/claude
( curl -sS -X POST http://127.0.0.1:11434/v1/chat/completions \
    -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    --data @/tmp/big-req.json -w '\n--- A %{http_code} %{time_total}s ---\n' ) &
( cat /tmp/big-prompt.txt | ANTHROPIC_BASE_URL=http://127.0.0.1:19999 \
    "$CLAUDE_BIN" -p --model claude-opus-4-7; echo "--- B exit $? ---" ) &
wait

# 4. stop the proxy and diff
kill %1
python3 /tmp/diff_headers.py
```

`diff_headers.py` lives at `/tmp/diff_headers.py` (regenerable from the
shape documented above); it reads `/tmp/anthropic-capture.ndjson` and
`~/.local/state/clyde/clyde.jsonl` and prints the marker-prefixed diff.
Update [internal/adapter/anthropic/client.go](../internal/adapter/anthropic/client.go)
or `[adapter.impersonation]` in your toml until the diff is empty.

## Terms of service

You are responsible for complying with Anthropic's ToS when driving
the subscription through automation. This tooling exists for the
same reasons other community bridges do; it is not an invitation to
abuse the service.
