# Adapter refactor research notes

This file keeps only product and protocol facts that are still useful for
implementation. Avoid adding session transcripts or dated progress logs here.

## Cursor Ingress

- Cursor sends OpenAI-compatible requests to `/v1/chat/completions`.
- The body is Responses-shaped: input items live in `input`, not `messages`.
- Cursor model names arrive as native-looking ids such as `gpt-5.4`,
  `gpt-5.5`, `claude-opus-4-7`, and `claude-haiku-4-5-20251001`.
- Cursor request ids and conversation ids live in metadata when present.
- Product tools include `Subagent`, `SwitchMode`, `AskQuestion`, `CreatePlan`,
  `ApplyPatch`, `CallMcpTool`, and `FetchMcpResource`.
- Mode and request-path semantics should be derived once in `cursor/`, not
  inferred later from prompt text.

## Codex Provider

- Live Codex transport is direct websocket:
  `wss://chatgpt.com/backend-api/codex/responses`.
- Continuation is same-connection only. Cross-process or cross-connection
  `previous_response_id` reuse with `store: false` is not supported upstream.
- Clyde should keep the per-conversation websocket session cache and chain
  `previous_response_id` only inside that cached connection.
- Codex identity headers include `originator`, `x-codex-turn-metadata`,
  `x-codex-installation-id`, and `x-codex-window-id`.
- Current open validation is about turn metadata parity, longer-session reuse,
  prompt-cache behavior, and token-drop evidence, not the old fingerprint
  matcher.
- Current long-turn logs show same-connection reuse through
  `adapter.codex.ws_session.taken` / `put` and high prompt-cache reads around
  160k-186k prompt tokens. Generation ids are still absent when Cursor only
  supplies `cursorConversationId` and `cursorRequestId` metadata.

## Anthropic Provider

- Claude Code native compatibility targets Anthropic-shaped endpoints via
  `ANTHROPIC_BASE_URL`, not an OpenAI base-url path.
- Clyde exposes native `/v1/messages` and `/v1/messages/count_tokens` beside
  the OpenAI facade.
- The observed basic `claude -p` path called `/v1/messages?beta=true` and did
  not call `/v1/models` or `/v1/messages/count_tokens`.
- `/v1/messages/count_tokens` is intentionally a typed `not_supported` stub
  until live Claude Code traffic proves a fuller implementation is needed.
- Native ingress should accept Claude-compatible ids and must not remap
  `opus`, `sonnet`, or `haiku` requests to GPT aliases.
- Native ingress should resolve Clyde aliases to upstream Claude ids before
  calling Anthropic, while preserving raw `claude-*` ids when Claude Code sends
  them directly.

## MITM Baselines

- The daemon-owned MITM listener is the intended source of truth for rolling
  local baselines under XDG state.
- Always-on capture files may mix providers, so baseline refresh needs typed
  provider filtering before extracting snapshots.
- Claude HTTP traffic can refresh Snapshot v2 baselines directly from the
  always-on capture store.
- Codex CLI still uses CONNECT tunnel mode in the current in-process proxy, so
  tunneled payload bytes are not available for rolling baseline refresh without
  a deeper interception layer.

## Error And Notice Handling

- Anthropic 429 maps to a retryable upstream error and OpenAI-native
  `rate_limit_error` / `rate_limit_exceeded` envelopes.
- Anthropic 200 responses with overage or rate-limit warning headers are
  successful responses with notices, not failures.
- Streaming errors after headers are committed should be surfaced as SSE error
  frames plus `[DONE]`, not as late HTTP JSON responses.

## Context And Capability Notes

- The `/v1/models` surface needs to report context budgets that match observed
  provider behavior closely enough for Cursor to make good decisions.
- Live `/v1/models` currently reports `1000000` context for 1m Opus aliases and
  `200000` for non-1m Opus aliases. Current long-turn logs are still below the
  advertised 1m budget, so they do not prove whether Cursor auto-summarization
  should already have triggered for 1m aliases.
- The older Codex HTTP characterization found about `272000` observed input
  context for `gpt-5.4` and `gpt-5.5`. Websocket context behavior still needs
  independent validation.
- `CLYDE-158` tracks context-window mismatch handling.
- `CLYDE-163` tracks Cursor auto-summarization not engaging for Clyde adapter
  models.
