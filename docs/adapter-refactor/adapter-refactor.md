# Adapter refactor

This is the current working plan for `internal/adapter/`. It is intentionally
short. Keep old implementation detail out of this file; use it to avoid
repeating completed work and to choose the next slice.

## Goal

`internal/adapter/` exposes an OpenAI Chat Completions compatible surface for
Cursor and an Anthropic-native ingress for Claude Code. The root adapter owns
HTTP decode, auth, model resolution, provider dispatch, and OpenAI response
encoding. Each provider owns request shaping, transport, upstream response
parsing, and provider-specific lifecycle.

## Current Architecture

| Package                       | Role                                                          |
| ----------------------------- | ------------------------------------------------------------- |
| `internal/adapter/`           | HTTP routes, auth, dispatch, server lifecycle                 |
| `internal/adapter/openai/`    | OpenAI wire types and SSE framing                             |
| `internal/adapter/cursor/`    | Cursor request normalization and product tool semantics       |
| `internal/adapter/resolver/`  | Model family, provider, effort, and context-budget resolution |
| `internal/adapter/provider/`  | Provider contract, event writer, shared results and errors    |
| `internal/adapter/anthropic/` | Anthropic OAuth provider and native `/v1/messages` transport  |
| `internal/adapter/codex/`     | Codex websocket provider, session cache, turn metadata        |
| `internal/adapter/render/`    | Normalized event model to OpenAI chunks                       |
| `internal/adapter/runtime/`   | Lifecycle logging and notice surfacing                        |

## Completed Memory

See [`adapter-refactor-history.md`](./adapter-refactor-history.md) for the
concise completed-task list. Do not re-open those tasks unless current code
evidence shows a regression.

## Next Slice

1. Continue `CLYDE-163`: add Clyde-side context preflight for Cursor/Codex
   requests because `/v1/models` context metadata alone did not make Cursor
   auto-summarize oversized `clyde-gpt-5.5` turns.
2. Do `CLYDE-171`: finish removing the remaining hard-coded native
   Codex/GPT alias catalog, context helpers, and routing defaults now that
   Clyde-specific GPT aliases can be declared in config. `CLYDE-169` is the
   umbrella tracker, not the consumable next ticket.
3. Continue `CLYDE-165`: move MITM baseline refresh under the daemon-owned
   always-on listener and keep local rolling baselines in XDG state.
4. Keep `CLYDE-162` open for a true Cursor `Subagent` tool repro. Current
   adapter logs show missing Cursor generation ids, but the latest turn did not
   include a `Subagent` tool in the request body.

## Global Remaining Set

- `CLYDE-134`: Done. Native ingress reaches real Anthropic OAuth; live bucket
  currently returns 429.
- `CLYDE-150`: In Progress. Keep Anthropic parity and generated wire identity
  in sync with the local XDG baseline.
- `CLYDE-151`: Todo. Validate Codex turn metadata against live ChatGPT Pro
  traces.
- `CLYDE-152`: Done. Websocket reuse and longer-turn stability look good enough
  to close.
- `CLYDE-153`: In Progress. Move remaining tests under provider, render,
  runtime, and root ownership boundaries.
- `CLYDE-154`: In Progress. Sweep dead imports and dead adapter-local types
  after bridge/fallback deletion.
- `CLYDE-155`: Todo. Generate provider wire types from `research/` and remove
  raw payload probing.
- `CLYDE-157`: In Progress. Add trace/span IDs across adapter, daemon,
  provider, and capture logs.
- `CLYDE-158`: In Progress. Fix or explain context-window mismatch behavior for
  Cursor adapter models.
- `CLYDE-159`: Todo. Reproduce Codex long-running tasks stopping too early.
- `CLYDE-160`: Todo. Reproduce Claude long-running tasks stopping too early.
- `CLYDE-161`: Todo. Split logging architecture by concern and evaluate
  per-request log bundles.
- `CLYDE-162`: In Progress. Needs a true Cursor `Subagent` request-body repro;
  current logs show missing generation ids only.
- `CLYDE-163`: In Progress. Investigate Cursor context auto-summarization not
  engaging for Clyde adapter models.
- `CLYDE-165`: In Progress. Daemon-owned always-on MITM with rolling XDG
  baselines, drift logs, and convenience-only CLI.
- `CLYDE-169`: In Progress. Clyde-specific GPT aliases and Claude family
  aliases are now config-declared, but native Codex alias/context helpers and
  source-level routing defaults still need to move behind config. Consumable
  child ticket: `CLYDE-171`.

## Non-Negotiables

- Do not reintroduce subprocess or app-server fallback paths.
- Do not add new root-owned Anthropic or Codex request builders.
- Keep OpenAI SSE framing in `openai/` and normalized event handling in
  `render/` plus provider writers.
- Keep provider-specific terminal-state mapping inside provider-owned code.
- Do not add `any`, `interface{}`, `map[string]any`, or `[]any` to touched
  production adapter code.

## Evidence Files

- [`adapter-refactor-research.md`](./adapter-refactor-research.md): compact
  product and protocol facts still useful for implementation.
- [`snapshot-v2-design.md`](./snapshot-v2-design.md): compact Snapshot v2
  status for `CLYDE-150`.
