# Claude Code Compaction Internals (Reverse Engineered)

Source: Claude Code 2.1.111, bundle at
`/Users/agoodkind/Library/Application Support/Claude/claude-code/2.1.111/claude.app/Contents/MacOS/claude`.
This is a single Mach-O file produced by Bun. The JS bundle was inspected via
`strings` and pattern matching for compaction-related identifiers.

This document explains how Claude Code marks, detects, trims, and resumes
across compaction boundaries in the JSONL transcript. It then specifies the
inverse operation (uncompact) that clyde must implement.

## Boundary Schema

When `/compact` runs (manual or auto), Claude Code writes one new line to
the active transcript JSONL. The exact shape is produced by `J__`:

```js
{
  type: "system",
  subtype: "compact_boundary",
  content: "Conversation compacted",
  isMeta: false,
  level: "info",
  uuid: "<random>",
  timestamp: "<iso>",
  sessionId: "<sess>",
  compactMetadata: {
    trigger: "manual" | "auto",
    preTokens: <number>,
    postTokens: <number>?,
    duration_ms: <number>?,
    userContext: <string>?,
    messagesSummarized: <number>?,
    preservedSegment: {           // optional. controls which range survives
      headUuid: "<uuid>",
      anchorUuid: "<uuid>",
      tailUuid: "<uuid>"
    }
  },
  logicalParentUuid: "<uuid>"?    // optional, written when caller passes it
}
```

Notes.
- `content` is the literal string `"Conversation compacted"`. The full summary
  is NOT written into the JSONL. It is regenerated in memory and sent only on
  the next API call.
- The boundary is `type: "system"`. It is NOT a user or assistant message.
- The boundary line is byte-identifiable by the substring `"compact_boundary"`
  (with surrounding quotes). This is what enables the buffer scanner to find
  it without parsing every line.

A second variant exists: `subtype: "microcompact_boundary"`. It uses the same
shape, but downstream renderers and trimmers ignore it (`return null`). It is
not currently a stop marker.

A third shape is what clyde itself injects via the `--summary` flag and
the strip workflow:

```js
{
  type: "user",
  isCompactSummary: true,
  isVisibleInTranscriptOnly: true,
  message: { role: "user", content: "<summary or placeholder>" },
  parentUuid: "<uuid>",
  promptId: "<uuid>",
  sessionId: "<sess>",
  uuid: "<uuid>",
  userType: "external"
}
```

Claude Code's detector ignores this clyde shape entirely. It is only
honored by clyde's own walker.

## Detection Sites

Every place Claude Code identifies a boundary, located in the bundle:

1. `_b8(line)`. Parses one JSONL line. Returns null when not a boundary,
   otherwise `{hasPreservedSegment}`.
   ```js
   if (_.type !== "system" || _.subtype !== "compact_boundary") return null;
   return { hasPreservedSegment: Boolean(_.compactMetadata?.preservedSegment) };
   ```

2. `ej(entry)`. In-memory predicate.
   ```js
   return entry?.type === "system" && entry.subtype === "compact_boundary";
   ```

3. `w$5(messages)`. Returns the index of the LAST boundary in an array, or
   `-1`. Loops from the end backward, calling `ej`.

4. `Kz(messages, _)`. Returns `messages.slice(lastBoundaryIndex)` so all
   messages before the last boundary are dropped. This is the in-memory trim
   used during message assembly.

5. `wbK(state, buf, q)`. Streaming buffer scanner that looks for the byte
   sequence `"compact_boundary"` while reading the file. On hit, parses the
   line via `_b8`. If parsed and no `preservedSegment`, sets
   `state.out.len = 0` (drops everything seen so far) and records
   `boundaryStartOffset`. If `preservedSegment` is present, keeps the buffer
   and proceeds to the preserved range walker.

6. `DV5(file, size, ...)`. Pre-compact skip path. Activated when the file
   exceeds a size threshold (constant `HdH`) AND when
   `CLAUDE_CODE_DISABLE_PRECOMPACT_SKIP` is not set. Scans the file
   tail-first looking for a boundary so it can skip loading pre-boundary
   bytes. Calls a reset callback that clears all in-memory maps for the
   prior chain.

7. Stream message handler (during live session). When a `compact_boundary`
   system message arrives via the streaming path, the handler does
   `mutableMessages.splice(0, indexOfBoundary)` to drop earlier entries.
   When the boundary has `preservedSegment.tailUuid`, it instead walks the
   parent chain from `tailUuid` back to `headUuid` and keeps that range.

8. `mQ(messages)`. Walks all messages collecting
   `compactMetadata.preCompactDiscoveredTools` from each boundary. Used to
   restore the tool roster across compaction.

9. `w` zod schema. Parses incoming SDK messages and validates the shape
   above. Field names use snake_case on the wire and camelCase in memory
   (`Ae_` and the inverse adapter handle the conversion).

## How Trimming Actually Runs

There are two trim paths.

A. Streaming buffer trim (file load on resume).
- The reader scans the file forward in 1 MiB chunks.
- For every newline, it checks the line's bytes for `"compact_boundary"`.
- On match, it parses the line.
- If the line is a real boundary AND `preservedSegment` is absent, the
  read-out buffer is reset. The reader then continues forward from the
  boundary line, accumulating everything that follows.
- If `preservedSegment` is present, the reader keeps the buffer and uses
  the preserved range walker (described below) to KEEP the headUuid -> tailUuid
  chain along with the post-boundary entries.
- Effect: only the suffix of the file beginning at (or just after) the LAST
  boundary survives. Preserved-segment entries from prior compactions are
  carried alongside.

B. In-memory trim (after a fresh boundary lands during a live turn).
- The mutableMessages array is spliced at the boundary index.
- Same preservedSegment treatment: walk parent chain from tailUuid back to
  headUuid, keep that range, drop everything else before the boundary.

There is no persistent boundary cache outside the JSONL. The file is the
truth on every resume. The PID-keyed files in `~/.claude/sessions/` are the
remote-control bridge files (unrelated to compaction). The per-session
directory `~/.claude/projects/<dir>/<sessionId>/` holds subagent transcripts
and externalized large tool results, also unrelated to compaction.

## Why Clyde's Lift May Appear Broken

`internal/transcript/compact.go LiftBoundary` mutates the boundary line in
place by deleting `subtype`, `isCompactSummary`, `isVisibleInTranscriptOnly`,
and `compactMetadata`. Then it rewrites `parentUuid` from
`logicalParentUuid` if the original was null.

After this mutation:
- `_b8` returns null because the subtype check fails.
- `ej` returns false for the same reason.
- `wbK` no longer finds the literal `"compact_boundary"` substring on this
  line because the field that contained those bytes was deleted.
- `Kz` no longer trims because no boundary remains in the array.

So the lift is structurally correct against every detection site. If the
user perceives the lift as broken, the most likely real cause is one of:

1. Auto-recompact on resume. After the lift, the chain length is large.
   The next API call computes tokens and may exceed `autoCompactThreshold`.
   Claude Code then triggers an auto-compact and writes a new boundary,
   which immediately undoes the lift. Set
   `CLAUDE_CODE_DISABLE_PRECOMPACT_SKIP=1` to disable the file-load skip,
   but this does NOT disable auto-compact. To suppress auto-compact, the
   environment variable to set is the threshold knob exposed via
   `autoCompactThreshold` config (negative means disabled, per the
   bundle: `autoCompactThreshold: $?.autoCompactThreshold ?? -1`).

2. A `microcompact_boundary` line that the lift does not target. Clyde's
   `FindBoundaries` only matches `compact_boundary` and `isCompactSummary`.
   Add `microcompact_boundary` to the predicate.

3. A residual `"compact_boundary"` byte sequence somewhere in the line.
   For example, a `compactMetadata.userContext` value that mentions the
   string. After clyde deletes `compactMetadata` this is gone, but if
   future versions move the metadata into a different surviving field the
   byte scanner could re-trip.

4. Multiple boundaries where lift only handled one. Lift-all does iterate
   all known boundaries, but if any non-standard variant is present
   (microcompact, or a future variant), it is missed.

5. The lifted file is not actually saved to the path Claude Code reads.
   Confirm by reading `f9d61101-...jsonl` mtime and content after lift.

In the current live transcript, every standard boundary has been lifted. A
subtype audit reports only `away_summary`, `bridge_status`, `informational`,
`local_command`, `stop_hook_summary`, `turn_duration` system entries, which
are non-trimming.

## Inverse Operation Specification (Uncompact)

Goal: given a JSONL transcript with N compaction boundaries, produce a file
that Claude Code loads as the FULL pre-compaction chain plus all post-compact
turns, and that does not auto-recompact on resume.

Required steps:

1. Identify every boundary line. The match predicate must cover:
   - `type === "system" && subtype === "compact_boundary"`
   - `type === "system" && subtype === "microcompact_boundary"`
   - `isCompactSummary === true` (clyde-injected variant)
   - Any future subtype that contains the substring `compact` and lives on
     a `system` entry. Treat as suspicious and require explicit allowlist.

2. For each boundary line, choose one of:

   a. Defuse in place. Mutate the line so:
      - `subtype` is deleted OR replaced with `compact_boundary_lifted`
        (changing the value still defuses because the byte scanner looks
        for the exact quoted bytes `"compact_boundary"` and the new value
        does not contain those exact bytes between two quotes).
      - `compactMetadata` is deleted entirely.
      - `isCompactSummary`, `isVisibleInTranscriptOnly`, and
        `logicalParentUuid` are deleted.
      - `uuid` is preserved so any post-boundary entry that sets
        `parentUuid: <boundaryUuid>` still resolves.
      - `parentUuid` is preserved as-is. Do NOT overwrite from
        `logicalParentUuid` unless `parentUuid` is null or empty AND
        `logicalParentUuid` exists. Otherwise the parent chain may
        regress.

   b. Remove the line and rewire. Delete the boundary line. For every
      subsequent entry whose `parentUuid` matches the boundary's `uuid`,
      reassign that entry's `parentUuid` to the boundary's own
      `parentUuid`. This collapses the boundary out of the chain.
      Recompute the chain integrity check after the rewrite.

   Defuse-in-place (option a) is preferred for active sessions because it
   preserves UUIDs that other components may reference (subagent transcripts,
   bridge state, audit log). Removal (option b) is preferred for offline
   archival where chain length matters.

3. Block auto-recompact. Two surfaces:
   - Set `autoCompactThreshold` config to `-1` for the resume command. The
     bundle uses `autoCompactThreshold ?? -1` and treats negative values
     as disabled.
   - Set env var `CLAUDE_CODE_DISABLE_PRECOMPACT_SKIP=1` to disable the
     pre-compact file-load skip path, so even if the file size triggers
     the skip threshold, the loader reads the full file.

4. Verify on disk. After the rewrite:
   - Re-grep the file for the literal substring `"compact_boundary"`.
     Expected count: 0.
   - Re-grep for `"isCompactSummary":true`. Expected: 0.
   - Walk every entry's `parentUuid` and confirm every non-null parent
     resolves to a UUID that exists in the file.
   - Count `type==="user"` and `type==="assistant"` entries. Confirm
     parity with the pre-strip backup.

5. Verify in process. After the next `claude --resume`:
   - Tail the log at `~/.claude/projects/<dir>/<sessionId>.jsonl` while
     resuming. Check that no new `compact_boundary` line is appended on
     load.
   - The first API call after resume should send the full chain. If the
     bundle is wrapped with a debugging proxy, capture the request body
     and confirm message count matches the post-rewrite file count.
   - If a new boundary appears, auto-recompact triggered. Re-check the
     threshold setting.

## Open Questions

- Where does Claude Code persist the user's chosen `autoCompactThreshold`?
  The bundle reads from a config object, but the config file path is not
  yet pinpointed. Likely `~/.claude/settings.json` under a key such as
  `autoCompactThreshold`. Needs confirmation by setting a value and
  diffing settings.
- Does `CLAUDE_AFTER_LAST_COMPACT` env var affect resume? It appears as a
  query parameter (`after_last_compact: true`) for a session API call. Its
  effect on the local trim path is not yet established.
- Is there a hard kill switch for auto-compact? The threshold approach
  works if -1 is honored everywhere, but a dedicated boolean would be
  safer.

## Implementation Checklist For Clyde

- [ ] Extend `FindBoundaries` to also match `microcompact_boundary` and
      to log any unknown `compact*` system subtype as a warning.
- [ ] Add an explicit `--no-auto-recompact` flag on `clyde resume` that
      sets `autoCompactThreshold: -1` for the spawned process.
- [ ] Add a verification pass after every lift or strip that re-greps the
      file for `"compact_boundary"` and fails loud if any remain.
- [ ] Replace the chain-walk fallback that overwrites `parentUuid` from
      `logicalParentUuid` with a guard that only fires when the original
      `parentUuid` is null or empty.
- [ ] Add a `clyde compact --audit` mode that prints every boundary,
      its byte offset, its detection-site reachability, and the chain
      ranges it shadows.
- [ ] When clyde injects its own boundary via `--summary`, write a
      schema that Claude Code recognizes (`type: "system"`,
      `subtype: "compact_boundary"`, `compactMetadata` populated) so the
      same defuse path works for both.
