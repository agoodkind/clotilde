# MITM Snapshot v2 design

To file as CLYDE Tack ticket. P1.

## Why this exists

The Snapshot type shipped with `CLYDE-144` was modeled around the Codex
websocket flow: one canonical handshake, one repeating frame body.
Empirical capture of Claude Code traffic on 2026-04-27 (from
`~/.local/state/clyde/mitm/always-on/capture-pre-fix.jsonl`) proved
the assumption is wrong for HTTP-based providers.

## Empirical findings driving v2

Multiple distinct caller flavors hit the same provider with
materially different wire shapes. Observed for `claude-cli/2.1.121`:

| flavor | beta flags | body keys | model | message count |
|---|---|---|---|---|
| compaction probe (`claude -p`) | 5 | `max_tokens, messages, metadata, model` | haiku | 1 |
| full interactive | 9 (adds claude-code-20250219, context-1m, advisor-tool, effort) | + context_management, output_config, stream, system, thinking, tools | opus | 645 |
| curl smoke (`curl ... -d '{}'`) | 0 | empty | none | 0 |

Likely more flavors not yet captured: `claude exec ...`,
fork-session probes, slash commands, agent-mode tool calls.

Nested body fields carry their own sub-shapes that the current
snapshot ignores entirely:

- `system`: array of objects with `text` and `cache_control` (per-entry caching).
- `tools`: array of tool definitions with `name`, `description`, `input_schema` (full JSON schema).
- `thinking`: object with `type`, `budget_tokens`.
- `output_config`, `context_management`, `metadata`: nested structured data with their own field sets.

Key-only field tracking (the v1 `Body.FieldNames`) loses all of this.

## v1 vs v2

**v1 (shipped):**
- Single `Handshake.Headers` taken from first `ws_start` only.
- `Body.FieldNames` is a union across all frames; loses per-flavor distribution.
- `Constants` are scalars from first record.
- No nested body sub-shape.
- Designed for Codex ws (one ws_start, repeating response.create frames).

**v2 (this design):**

```
Snapshot v2 = {
  Upstream: { name, captured_at, capture_count }
  Flavors: [
    FlavorShape {
      Signature: { user_agent_pattern, beta_fingerprint, body_key_set }
      RecordCount: int
      Handshake: {
        Headers: [
          { name, observed_values, presence (required|optional),
            classification (constant|enum|free) }
        ]
      }
      Body: {
        Type: string
        Fields: [
          { name, presence, value_kind (string|number|object|array|enum),
            sub_shape (recursive) }
        ]
      }
      FrameSequence: { ... existing ... }
    }
  ]
  Constants: { ... unioned across flavors ... }
}
```

## Sequencing

**v1.5 (this session, option 1).** A per-flavor classifier feeds the
EXISTING Snapshot type. Each captured caller flavor produces its
own `reference.toml` under `research/<upstream>/snapshots/<flavor>/`.
This unblocks `CLYDE-150` (Anthropic byte-identical parity) without
waiting for v2's richer schema. The classifier becomes the natural
feeder for v2: same signature math, just emits richer output.

**v2 (this session, option 2).** New typed `Snapshot v2` shape with
nested sub-shape recording, header classification (constant vs enum
vs free), and per-flavor full extraction.

## Acceptance for v2

1. New `internal/mitm/snapshot_v2.go` with the typed `Snapshot v2` shape.
2. `ExtractSnapshotV2` reads a JSONL transcript and emits a v2
   Snapshot grouping records into FlavorShapes by signature.
3. Per-flavor body sub-shape: `system`, `tools`, `thinking`,
   `output_config`, `context_management`, `metadata` get their
   nested structure recorded with depth 2-3.
4. Header classification surfaces constants (always same value) vs
   enums (small observed set) vs free-form (high cardinality,
   redact to type).
5. `clyde mitm snapshot` gains `--v2` flag to opt in. v1 stays for
   Codex backward compat.
6. `clyde mitm diff` understands both v1 and v2 schemas; v2 diff
   is per-flavor.
7. `CLYDE-150`'s parity check uses v2 against the captured Claude
   Code reference.

## Out of scope

- Codex ws schema migration. Codex stays on v1 because the
  assumption (one canonical shape) is empirically true there.
- Body redaction policy beyond what existed in v1.
- Auto-classification of "new flavor seen" alarms; that's a
  downstream alert layer.

## References

- `internal/mitm/schema.go` v1 `Snapshot` type.
- `internal/mitm/snapshot.go::ExtractSnapshot` v1 extractor.
- `~/.local/state/clyde/mitm/always-on/capture-pre-fix.jsonl` for the
  multi-flavor evidence.
- `CLYDE-150` (Anthropic byte-identical) is the immediate consumer.
