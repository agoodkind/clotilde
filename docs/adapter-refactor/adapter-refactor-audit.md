# Adapter refactor plan audit

Date: 2026-04-26

Audited file:

- `docs/adapter-refactor/adapter-refactor.md`

## Summary

The plan has become three documents in one:

1. Target architecture and phase plan.
2. Historical research notes and live-observation evidence.
3. Progress log / TODO tracker.

That is why it repeats itself and drifts. The main plan is 3,090 lines, while
the progress log is another 1,606 lines. The plan now contains current-state
claims, completed-history claims, active workstream notes, and prioritized
TODOs that often restate the same facts in different eras of the refactor.

## High-signal problems

### 1. Stale file references are mixed with current truth

Examples in `adapter-refactor.md`:

- line 57 still says Cursor-to-Codex mapping lives in
  `internal/adapter/codex_handler.go`; that file has been deleted.
- line 238 lists `internal/adapter/codex_handler.go` as observed Codex request
  shaping; that file no longer exists.
- line 986 still names `tooltrans.TranslateRequest(...)` as the Anthropic
  request marshalling path; Anthropic owns that in
  `internal/adapter/anthropic/backend/mapper_impl.go`.
- lines 1316-1319 describe `tooltrans/types.go`,
  `tooltrans/openai_to_anthropic.go`, `tooltrans/stream.go`, and
  `tooltrans/event_renderer.go`; those files have been moved or deleted.
- line 1404 still references `internal/adapter/tooltrans/..._test.go`; the
  only remaining `tooltrans` test is the sentinel strip test.

Current reality:

- `internal/adapter/tooltrans/` contains only `doc.go`, `sentinels.go`, and
  `thinking_strip_test.go`.
- `internal/adapter/codex_handler.go`, `internal/adapter/codex_handler_test.go`,
  and `internal/adapter/fallback_handler.go` are gone.
- `internal/adapter/anthropic/backend/mapper.go` is gone; mapper
  implementation is in `mapper_impl.go`.

### 2. Status is duplicated in too many places

The same progress appears in at least five sections:

- early "Current state" and "What is already in place"
- per-phase sections
- `Implementation todos`
- `Current status`
- `Prioritized TODO queue`

This makes any update incomplete by default. A phase can be marked done in one
place, partial in another, and stale in the top-level current-state narrative.

### 3. Research/evidence blocks are too large for an execution plan

The Cursor, Codex parity, Anthropic notice/error, and research-source sections
are useful, but they make the main plan hard to scan. They should be archived
or split into support docs, then referenced from the plan.

Suggested split:

- `adapter-refactor.md`: canonical current plan and active TODOs only.
- `adapter-refactor-research.md`: observed Cursor/Anthropic/Codex internals,
  request shapes, links into `research/`.
- `adapter-refactor-history.md`: completed milestones and old phase notes.
- `last_agent_progress_apr_26_2026.md`: append-only session progress log only.

### 4. Phase text mixes target state and implementation history

Several phase sections start as "planned split" or "current state" but now
contain completed implementation details. That creates long sections that are
neither a plan nor a changelog.

Fix: each phase should be a compact table:

| Phase | Status | Done | Remaining | Blocked by |
| --- | --- | --- | --- | --- |

Detailed completed notes should move to history/progress.

### 5. TODO queue is not the single source of truth

The "Prioritized TODO queue" is useful, but it still restates TODOs that also
exist in phase sections. It should become the only operational task list.

Recommended rule:

- Phase sections explain intent and ownership boundaries.
- Prioritized TODO queue lists all remaining work.
- Completed work appears only in history/progress, not in the live task list.

## Recommended cleanup plan

### Batch 1: make the current plan trustworthy

1. Replace the top-level "Current state" with a short generated snapshot:
   active packages, deleted root shims, remaining root bridges.
2. Remove or archive stale references to deleted files.
3. Rewrite Phase 8 and Phase 9 sections to match current `tooltrans` reality.
4. Collapse repeated "done 2026-04-26" bullets into one history pointer.

Expected result: cut roughly 700-1,000 lines without changing any technical
direction.

### Batch 2: split research from execution

1. Move Cursor observed contract details and tool inventory into
   `adapter-refactor-research.md`.
2. Move Codex app parity evidence and Anthropic notice/error evidence into the
   same research doc or provider-specific subsections.
3. Keep only short links from the main plan to those research sections.

Expected result: the main plan becomes readable without losing evidence.

### Batch 3: create a canonical TODO table

1. Replace the per-phase TODO blocks and prioritized TODO queue with one
   canonical task table.
2. Use statuses: `todo`, `active`, `blocked`, `deferred`.
3. Add columns for owner package, acceptance test, and dependency.

Expected result: no more drift between "Phase N todos" and "Prioritized TODO
queue."

### Batch 4: archive completed phase history

1. Move old completed bullets into `adapter-refactor-history.md`.
2. Keep a short "Completed milestones" table in the main plan.
3. Stop appending detailed checkpoint text to the main plan; use the progress
   log for that.

Expected result: the plan stays stable as an execution document.

## Proposed final shape for `adapter-refactor.md`

Target size: 500-800 lines.

1. Goal and definition of done.
2. Current architecture snapshot.
3. Target architecture diagram.
4. Active ownership boundaries.
5. Remaining work table.
6. Phase notes, one compact section per phase.
7. Links to research/history/progress docs.

## Immediate recommendation

Do not do a piecemeal line edit. The document needs a structural cleanup pass:

1. Create `adapter-refactor-history.md` and `adapter-refactor-research.md`.
2. Move non-operational content out of the main plan.
3. Rebuild the main plan from the current code state plus the active TODOs.

That is safer than trying to manually prune repeated paragraphs in place.
