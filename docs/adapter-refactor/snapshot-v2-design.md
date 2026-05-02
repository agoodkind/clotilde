# Snapshot v2 status

`CLYDE-150` owns Anthropic parity and generated wire identity checks. `CLYDE-165`
owns the daemon-backed always-on MITM baseline architecture.

## Current State

- MITM capture tooling exists.
- Snapshot v2 extraction exists.
- `clyde mitm diff` can auto-detect Snapshot v2 references.
- `wire-snapshot-check` can use `reference-v2.toml` plus `--v2` when that
  reference exists.
- Claude Code Snapshot v2 baselines are local XDG-state artifacts under
  `XDG_STATE_HOME/clyde/mitm-baselines/`.
- Daemon startup owns the always-on MITM listener when `[mitm].enabled_default`
  is set.
- Baselines refresh from accumulated captures on daemon-owned drift ticks and
  debounced capture callbacks, with drift logged before replacement.
- `internal/adapter/anthropic/wire_flavors_gen.go` is generated from that
  Snapshot v2 reference.
- Direct v2 diff is clean against the latest local Claude Code MITM reference.

## Still Needed

1. Keep real Clyde-to-Anthropic OAuth validation separate from zero-divergence
   Snapshot v2 diffing, because live runs can terminate at upstream bucket
   limits even when wire shape is correct.

## Why V2 Exists

Claude Code HTTP traffic has multiple caller flavors, while the older snapshot
shape was built around Codex websocket traffic. Snapshot v2 groups captures by
flavor and records header presence plus nested body shape, including fields
such as `system`, `tools`, `thinking`, `output_config`, `context_management`,
and `metadata`.

## Out Of Scope

- Codex websocket schema migration.
- New body-redaction policy.
- Automatic "new flavor seen" alarms.
