# Session Provider Refactor Master Plan

This document is the source of truth for the Clyde session-provider refactor.
Its job is to make the session stack provider-neutral first, then move Claude
behind that abstraction, then add Codex cleanly. It is intentionally broader
than the Codex MVP plan because the main risk is not Codex itself. The main risk
is keeping Claude semantics as the hidden default model and then bolting Codex
onto that shape.

This is a planning document, not an append-only history log. When a concrete
slice lands, summarize the result in a dated entry elsewhere and keep this file
focused on architecture, phases, and boundaries.

## Goal

`clyde` should own a generic session domain and a generic session lifecycle.
Providers should own provider-native identity, discovery, launch, resume,
cleanup, settings, and optional capabilities such as transcript history or
remote control. `cmd/`, `internal/ui/`, and `internal/daemon/` should talk to
that generic boundary rather than speaking Claude-specific concepts directly.

Definition of done:

1. `cmd/` launches and resumes sessions through a provider-neutral contract.
2. `internal/session/` models provider-neutral session state and provider-owned
   extensions without treating Claude as the default schema.
3. Claude is one provider implementation behind the abstraction, not the
   implicit core behavior.
4. Codex can be added as a second provider without widening Claude-shaped
   conditionals in `cmd/`, `internal/session/`, `internal/ui/`, or
   `internal/daemon/`.
5. Provider-specific capabilities such as transcript parsing, remote control,
   settings persistence, and identity rollover are explicit rather than
   ambient assumptions.

## Problem Statement

The repo currently mixes three layers that should be separate:

1. Clyde-owned session concepts:
   `name`, workspace association, context, parent linkage, list/search/rename
2. Provider-owned lifecycle concepts:
   session identity, launch/resume command shape, discovery, cleanup,
   provider-specific settings
3. Claude-specific implementation details:
   `sessionId`, `transcriptPath`, `previousSessionIds`, `settings.json`,
   SessionStart hook adoption, remote control persistence

The current risk is not just that some fields are named after Claude. The
deeper problem is that orchestration above the provider layer still knows
Claude-only follow-through. The current `cmd/root.go` path still knows that a
new Claude session with remote control enabled requires a post-launch Claude
settings persistence step. That means the provider boundary is not yet real.

## Before / Current / After

```mermaid
flowchart LR

subgraph beforeState [Before]
    CmdBefore["cmd/root.go"]
    SessionBefore["internal/session (Claude-shaped core)"]
    ClaudeBefore["internal/claude"]
    ClaudeConceptsBefore["Claude concepts:
sessionId
transcriptPath
previousSessionIds
settings.json
remoteControl"]
    CmdBefore --> SessionBefore
    CmdBefore --> ClaudeBefore
    SessionBefore --> ClaudeConceptsBefore
    ClaudeBefore --> ClaudeConceptsBefore
end

subgraph currentState [Current]
    CmdCurrent["cmd/root.go"]
    SessionCurrent["internal/session (partly generic)"]
    ClaudeCurrent["internal/claude helpers"]
    CmdCurrent --> SessionCurrent
    CmdCurrent --> ClaudeCurrent
    CmdCurrent -->|"still knows remoteControl persistence"| ClaudeCurrent
    SessionCurrent -->|"provider/id abstractions starting to exist"| ClaudeCurrent
end

subgraph targetState [After]
    CmdAfter["cmd/root.go"]
    SessionCore["generic session core"]
    ProviderBoundary["session provider boundary"]
    ClaudeProvider["Claude provider impl"]
    CodexProvider["Codex provider impl"]
    CmdAfter -->|"generic launch/resume intent"| SessionCore
    SessionCore --> ProviderBoundary
    ProviderBoundary --> ClaudeProvider
    ProviderBoundary --> CodexProvider
end
```

## Concrete Boundary Rules

The refactor should enforce these rules:

### Clyde Core Owns

- Stable Clyde session name
- Display title surfaced to UI
- Workspace root and working directory
- Created / last accessed timestamps
- Parent session linkage at the Clyde layer
- Session list, search, rename, and delete orchestration
- Capability-aware UI and daemon behavior

### Provider Layer Owns

- Provider-native session identity
- Launch and resume semantics
- Discovery and adoption
- Provider-specific settings persistence
- Provider-specific cleanup
- Provider-specific history/transcript location and parsing
- Provider-specific lifecycle features such as remote control or fork lineage

### `cmd/` Must Not Know

- Whether provider identity is a UUID
- Whether a provider rotates IDs after clear/compact
- Whether a provider uses `settings.json`
- Whether a provider stores transcripts under `~/.claude/projects` or anywhere
  else
- Whether a provider needs post-launch persistence for a feature like remote
  control

If `cmd/` needs to branch on one of those things, the boundary is still wrong.

## Current Leak Inventory

These are the main concrete leaks to remove.

### `internal/session/session.go`

`Metadata` still centers Claude-shaped fields:

- `SessionID`
- `TranscriptPath`
- `PreviousSessionIDs`
- `Settings` assumptions nearby

Even with `Provider` added, the schema still reads like "Claude metadata plus
one provider field" rather than "generic session row with provider-owned
extensions."

### `internal/session/store.go`

The old version treated direct ID resolution as UUID-only. That is already being
worked on, but the deeper generic rule is:

- identity lookup must use provider-aware exact identifiers
- dedupe must key by provider plus provider-native session ID
- adoption must stop assuming one global Claude ID namespace

### `internal/session/scan.go`

The old file was a Claude transcript walker masquerading as generic session
discovery. The new direction is correct: provider scanner boundary first, Claude
scanner behind it. The remaining requirement is to make sure the scanner
abstraction uses the same provider identity model as the session core, not a
parallel one that drifts.

### `internal/claude/invoke.go`

This is the right package to own Claude-specific launch and resume behavior.
However, the abstraction is not complete until the full Claude follow-through
also lives here or in a Claude-owned provider package.

### `cmd/root.go`

The current leak is the best example of what still needs to move down:

```988:1006:cmd/root.go
err = claude.StartNewInteractive(env, "", workDir, enableRemoteControl, sessionID)
if err != nil {
    return err
}
sess, gerr := store.Get(name)
if gerr == nil && sess != nil {
    if enableRemoteControl {
        if err := claude.PersistRemoteControlSetting(store, name); err != nil {
            // ...
        } else {
            // ...
        }
    }
}
```

The file no longer writes Claude settings directly, which is an improvement, but
it still knows:

- the feature is `remoteControl`
- it is a Claude session setting
- it requires post-launch persistence
- the relevant helper is `claude.PersistRemoteControlSetting(...)`

That orchestration belongs below the provider boundary.

## Target Architecture

```mermaid
flowchart TD
    Cmd["cmd/"]
    SessionDomain["internal/session
generic domain"]
    ProviderBoundary["provider lifecycle boundary"]
    Discovery["provider discovery boundary"]
    ClaudeProvider["Claude provider"]
    CodexProvider["Codex provider"]
    UI["internal/ui"]
    Daemon["internal/daemon"]

    Cmd --> SessionDomain
    UI --> SessionDomain
    Daemon --> SessionDomain
    SessionDomain --> ProviderBoundary
    SessionDomain --> Discovery
    ProviderBoundary --> ClaudeProvider
    ProviderBoundary --> CodexProvider
    Discovery --> ClaudeProvider
    Discovery --> CodexProvider
```

The key idea is that `internal/session` becomes the generic domain and contract
surface, not the place where provider-specific behavior accumulates.

## Proposed Types And Interfaces

These are concrete sketches, not final signatures.

### Core Session Row

A generic session row should look roughly like:

```go
type Session struct {
    Name     string
    Metadata Metadata
}

type Metadata struct {
    Name          string
    Provider      session.ProviderID
    DisplayTitle  string
    WorkspaceRoot string
    WorkDir       string
    Created       time.Time
    LastAccessed  time.Time
    ParentSession string
    Context       string

    Identity      ProviderIdentity
    Capabilities  ProviderCapabilities
    ProviderState ProviderState
}
```

The exact field breakdown can vary, but the important constraint is that the
generic row should stop pretending Claude transcript and rollover fields are
universal.

### Provider Identity

Provider identity needs to be first-class and typed:

```go
type ProviderSessionID struct {
    Provider ProviderID
    ID       string
}

type SessionIdentity struct {
    Current  ProviderSessionID
    Previous []ProviderSessionID
}
```

The repo is already moving in this direction in `internal/session/identity.go`.
The next step is to make every caller use those helpers rather than reading raw
`SessionID` and `PreviousSessionIDs` directly.

### Provider Lifecycle Boundary

The boundary above provider implementations should be explicit and typed:

```go
type LaunchOptions struct {
    WorkDir string
    Intent  LaunchIntent
}

type ResumeOptions struct {
    CurrentWorkDir string
    EnableSelfReload bool
}

type SessionProvider interface {
    ProviderID() ProviderID
    Capabilities() ProviderCapabilities
    StartInteractive(ctx context.Context, req StartRequest) error
    ResumeInteractive(ctx context.Context, sess *session.Session, req ResumeRequest) error
    DeleteProviderArtifacts(ctx context.Context, sess *session.Session) error
}
```

The exact names can change. The point is that `cmd` should invoke generic
operations and stop stitching together provider-specific follow-through.

### Discovery Boundary

Provider discovery should be isolated from the core session store:

```go
type DiscoveryScanner interface {
    Provider() ProviderID
    Scan() ([]DiscoveryResult, error)
}
```

That is already the correct conceptual direction for `internal/session/scan.go`.
The main caution is to keep discovery using the same provider and identity types
as the core session model.

## Phase Plan

### Phase 1. Stabilize The Generic Session Domain

Goal:
make `internal/session` clearly generic before moving more behavior.

Scope:

- finish provider-aware identity helpers in `internal/session/identity.go`
- make `internal/session/session.go` stop reading as "Claude metadata plus
  provider tag"
- define one provider identity model and reuse it everywhere
- make `internal/session/store.go` use that model consistently

Exit criteria:

- no UUID-only assumptions in core lookup paths
- no parallel identity models between discovery and store
- dedupe keyed by provider plus provider session ID

### Phase 2. Push Claude Lifecycle Fully Below The Boundary

Goal:
remove Claude follow-through from `cmd/`.

Scope:

- convert `internal/claude/invoke.go` from helper bag into a Claude lifecycle
  implementation
- move remote-control persistence and any Claude-specific post-launch
  reconciliation out of `cmd/root.go`
- make `cmd/root.go` express only generic launch intent

Exit criteria:

- `cmd/root.go` does not mention Claude-only persistence steps
- `cmd/root.go` does not know about `remoteControl` as a Claude settings detail

### Phase 3. Finish Provider-Scoped Discovery

Goal:
make Claude discovery the first provider implementation rather than the default.

Scope:

- keep `internal/session/scan.go` provider-oriented
- keep Claude transcript parsing in `internal/session/scan_claude.go`
- make the cache operate over a scanner set rather than one Claude root
- make adoption key by provider-aware identity

Exit criteria:

- adding `scan_codex.go` later does not require redesigning the cache or adopt
  loop

### Phase 4. Normalize Provider-Owned Settings And Cleanup

Goal:
stop treating session settings and provider artifact cleanup as generic.

Scope:

- move provider-specific settings persistence behind provider-owned boundaries
- move provider-specific cleanup behind provider-owned boundaries
- keep generic delete orchestration in Clyde core only

Exit criteria:

- no core code assumes `settings.json` is the provider settings format
- no core code assumes transcript cleanup semantics are Claude semantics

### Phase 5. Add Codex As A Second Provider

Goal:
prove the boundary is real by adding Codex with minimal cross-provider edits.

Scope:

- Codex discovery scanner
- Codex launch/resume implementation
- provider-aware store/adoption wiring
- capability gating in UI and daemon for unsupported Codex features

Exit criteria:

- Codex support lands mostly in new provider-owned files
- Claude files require only registration or minor shared-boundary changes

## Execution Order

1. Finish the generic session and identity model in `internal/session`.
2. Remove Claude-specific orchestration from `cmd/root.go`.
3. Finish the provider scanner split and adoption identity model.
4. Push settings and cleanup semantics behind provider-owned contracts.
5. Add Codex discovery and lifecycle as the second provider.
6. Gate transcript-dependent UI and daemon features by provider capability.

## Waterfall Execution Plan

The repo does not need PR-sized decomposition here. The largest coherent chunk
to pull forward is the full pre-Codex boundary cleanup: finish the generic core,
push Claude below the boundary, and finish provider-scoped discovery before
touching Codex. That is one waterfall block with ordered internal milestones,
not three unrelated parallel tracks.

### Active Waterfall Block

This block corresponds to the currently relevant tickets:

- `CLYDE-140` Extract provider-neutral session core
- `CLYDE-138` Move Claude lifecycle fully below provider boundary
- `CLYDE-141` Finish provider-scoped discovery and adoption

These three tickets should be treated as one dependency chain:

1. `CLYDE-140` establishes the generic session and identity model.
2. `CLYDE-138` proves the provider boundary by removing Claude orchestration
   from `cmd/`.
3. `CLYDE-141` finishes the provider discovery/adoption seam so a second
   provider can plug in without redesign.

Codex should not start until this block is materially complete.

### Largest Coherent Chunk

The largest chunk we can execute cleanly right now is:

1. make `internal/session` own one generic identity model
2. make `cmd/` stop knowing Claude-only follow-through
3. make discovery/adoption use the same provider identity model

This is the maximum safe chunk because all three parts depend on the same
boundary decisions:

- what the generic session row is
- what the provider identity type is
- where lifecycle orchestration belongs
- how discovery identities map back to persisted session rows

If we split that chunk too early, we risk hardening the wrong abstraction in one
layer and then rewriting it in the next.

### Internal Sequence For The Active Block

#### Step 1. Freeze The Generic Identity Model

Primary files:

- `internal/session/session.go`
- `internal/session/identity.go`
- `internal/session/provider.go`
- `internal/session/store.go`

Concrete objectives:

- define one typed provider identity model
- define one normalized provider discriminator
- make `Session`, `Metadata`, and dedupe semantics depend on those types
- stop UUID parsing from being the gate for exact-ID resolution

Entry criteria:

- the current branch still has Claude-shaped metadata or callers reading raw
  `SessionID` and `PreviousSessionIDs`

Exit criteria:

- one identity type is authoritative
- exact-ID lookup is provider-aware
- dedupe keys on provider plus provider session ID
- no second identity model exists in discovery code

#### Step 2. Push Claude Lifecycle Below The Boundary

Primary files:

- `cmd/root.go`
- `internal/claude/invoke.go`
- any new Claude-owned lifecycle helper file if needed

Concrete objectives:

- make `cmd/root.go` express generic launch/resume intent only
- move Claude-specific post-launch reconciliation into Claude-owned code
- move Claude-specific settings persistence behind Claude-owned boundaries
- remove knowledge in `cmd/` of Claude-only feature follow-through such as
  remote-control persistence

Entry criteria:

- `cmd/` still names Claude-only persistence or lifecycle steps

Exit criteria:

- `cmd/root.go` does not mention Claude-only persistence helpers
- `cmd/root.go` does not branch on Claude settings semantics
- `internal/claude` owns the full Claude launch/resume follow-through

#### Step 3. Unify Discovery And Adoption Around The Same Identity Model

Primary files:

- `internal/session/scan.go`
- `internal/session/scan_claude.go`
- `internal/session/cache.go`
- `internal/session/store.go`

Concrete objectives:

- keep scanner orchestration generic
- keep Claude transcript parsing in Claude-owned scanner code
- make discovery results carry the same provider identity vocabulary used by the
  persisted session model
- make adoption and reconciliation key on provider-aware identity

Entry criteria:

- discovery still carries a parallel provider or session-key model
- adoption still assumes one Claude ID namespace

Exit criteria:

- scanner cache operates over provider scanners
- discovery results map directly into persisted provider identity
- a future `scan_codex.go` can plug in without redesign

### What This Block Must Not Do

While executing the active waterfall block, do not:

- add Codex-specific logic to `cmd/`
- add `if provider == "codex"` branches in generic layers
- invent provider-specific metadata shapes in multiple places
- let `internal/session` and discovery use different provider identity types
- start UI/daemon capability gating before the provider boundary is stable

### Hand-off To The Next Block

Once the active block is complete, the next waterfall block becomes:

- `CLYDE-137` Push settings and cleanup behind provider-owned contracts
- `CLYDE-139` Add Codex CLI session provider implementation
- `CLYDE-136` Gate UI and daemon session features by provider capability
- `CLYDE-142` Add provider-boundary regression tests

That next block should start only after the pre-Codex boundary cleanup is
stable enough that Codex can land against one clear abstraction.

## Boundary Checklists

### A change is good if it does this

- replaces a Claude noun with a provider-neutral contract
- moves provider-specific persistence below the provider layer
- moves direct field access onto typed identity helpers
- reduces `cmd` knowledge of provider-specific follow-through

### A change is bad if it does this

- adds `if provider == "codex"` in `cmd/`
- adds more Claude-shaped fields to generic session metadata
- duplicates identity logic across store, discovery, and hook code
- introduces a second provider abstraction instead of using one consistent
  boundary

## Open Design Questions

These need to be answered deliberately as we work through the slices:

1. Should provider-specific persisted state live inline in `Metadata`, or in a
   typed nested provider state block?
2. Should launch/resume orchestration live in `internal/session`, a new
   `internal/session/provider` package, or stay in provider packages with a
   small shared interface?
3. Should `remoteControl` become a generic capability flag with provider-owned
   implementation, or remain a Claude-only capability surfaced generically to
   the UI?
4. How much provider-specific history capability do we want in the generic
   session model before Codex transcript parity work begins?

## Recommended Boundary Placement

The recommended placement is:

- `internal/session` owns the generic session domain and identity model
- provider packages own lifecycle implementation
- a very small shared provider contract lives next to the generic session domain
  rather than inside `cmd/`

That means the boundary should not live entirely inside `internal/session`, and
it should not remain an informal set of helpers spread across provider packages.
The cleanest shape is:

1. `internal/session` remains the source of truth for generic session records,
   identity, storage, and capability vocabulary
2. a small provider-facing contract lives close to that domain, either:
   in `internal/session/provider.go`, or
   in a tiny sibling package such as `internal/session/provider/`
3. concrete lifecycle behavior remains in provider packages such as
   `internal/claude` and later `internal/codex`

This is the recommended split because it preserves one generic vocabulary
without forcing the generic domain package to own provider-specific process
execution.

### Why This Placement Is Preferred

#### Option A. Put Lifecycle Directly In `internal/session`

This is not preferred.

Why:

- `internal/session` would stop being a pure domain and storage package
- process launch, provider-specific settings, and provider-specific cleanup
  would start accreting there
- the package would become the new place Claude semantics leak into, even if the
  names become slightly more generic

This option reduces surface movement in the short term, but it weakens the final
architecture.

#### Option B. Put Only Tiny Interfaces Near The Session Domain

This is preferred.

Why:

- `internal/session` keeps ownership of generic concepts:
  identity, metadata, capability vocabulary, discovery result shape, store
  semantics
- provider implementations can import those types and satisfy the contract
  without forcing `cmd/` to know provider-specific follow-through
- `cmd/` can depend on one generic lifecycle surface while Claude and Codex own
  their own implementation details below it

This gives us one shared language without centralizing provider behavior in the
wrong package.

#### Option C. Leave The Boundary Entirely Inside Provider Packages

This is only partly acceptable and not preferred as the final shape.

Why:

- it is better than leaking provider behavior into `cmd/`
- but without a small shared contract, the repo tends to drift into “Claude
  helper patterns” that Codex later imitates loosely instead of implementing one
  stable abstraction
- discovery, lifecycle, and cleanup can end up with parallel shapes instead of
  one intentional interface

This option is acceptable only as a temporary step while extracting the final
boundary.

### Recommended Concrete Shape

The recommended end state for the active waterfall block is:

```mermaid
flowchart TD
    Cmd["cmd/"]
    SessionDomain["internal/session
generic domain + identity + store"]
    ProviderContract["small provider contract"]
    ClaudeLifecycle["internal/claude
Claude lifecycle impl"]
    CodexLifecycle["internal/codex
Codex lifecycle impl"]

    Cmd --> ProviderContract
    ProviderContract --> SessionDomain
    ProviderContract --> ClaudeLifecycle
    ProviderContract --> CodexLifecycle
```

Interpretation:

- `cmd/` should call the small provider contract
- the provider contract should use `internal/session` types
- `internal/claude` should implement Claude-specific lifecycle and persistence
- `internal/codex` should later implement Codex-specific lifecycle against the
  same contract

### What The Contract Should Cover

The shared contract should be narrow. It should cover only:

- start interactive session
- resume interactive session
- provider capability description relevant to orchestration
- provider-specific post-launch reconciliation if needed
- provider-specific artifact cleanup entrypoint

It should not cover:

- transcript parsing internals
- provider wire formats
- UI rendering
- provider-specific transport details

Those stay in provider-owned code.

### Consequence For The Active Waterfall Block

This recommendation changes the implementation emphasis of the first block in a
useful way:

#### `CLYDE-140`

Should end with:

- one generic identity model
- one generic capability vocabulary
- one session-domain-owned type surface that provider implementations can consume

#### `CLYDE-138`

Should end with:

- `cmd/root.go` calling a generic lifecycle contract instead of Claude helpers
- `internal/claude` owning remote-control persistence and any Claude-specific
  post-launch follow-through

#### `CLYDE-141`

Should end with:

- discovery results and adoption using the same generic identity vocabulary as
  the provider contract and session store

### Litmus Test

The boundary placement is correct when this statement becomes true:

`cmd/` can start or resume a session without knowing whether the provider uses a
UUID, a rollout path, a `settings.json` file, remote-control persistence, or a
SessionStart hook.

If any of those details still live in `cmd/`, the provider lifecycle boundary is
still in the wrong place.

## Immediate Next Slice

The first concrete cleanup to make now is small and high leverage:

1. remove the Claude-only post-launch remote-control persistence from
   `cmd/root.go`
2. move that follow-through fully into `internal/claude`
3. keep `cmd/root.go` at the level of generic launch intent plus generic
   post-launch Clyde behavior

That slice is a good litmus test. If it feels awkward to push down, the
boundary is still not defined clearly enough.

## Related References

- Existing Codex session MVP plan:
  `/Users/agoodkind/.cursor/plans/codex_session_plan_17b2ca73.plan.md`
- Existing adapter refactor execution plan:
  [`adapter-refactor.md`](./adapter-refactor.md)
- Existing adapter refactor history:
  [`adapter-refactor-history.md`](./adapter-refactor-history.md)
