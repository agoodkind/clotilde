// Package contextusage is the unified context-retrieval layer for clyde.
//
// Every caller that needs a session's context-window usage or a token
// count for a synthetic payload goes through this package. The layer
// parallels store.Resolve in internal/session: a single entry point
// that encapsulates caching, backend selection, and shape translation,
// so call sites never talk to probes, count_tokens HTTP, or fixed-
// multiplier heuristics directly.
//
// # Exactness invariant
//
// Layer.Usage returns the same numbers Claude's /context slash command
// prints. By construction. The probe backend spawns claude with
// --resume --input-format stream-json --no-session-persistence and
// issues a get_context_usage control request. Claude itself runs
// collectContextData (commands/context/context-noninteractive.ts),
// which runs the exact same code path /context runs inside the live
// chat. The control response carries the ContextData payload verbatim.
// Any divergence between Layer.Usage and a live /context is a bug in
// the probe transport, not in the numbers.
//
// Layer.Count answers a different question ("how many tokens will this
// specific synthetic payload cost"). It routes to the Anthropic
// count_tokens HTTP endpoint. It is used by the planner target loop
// where the content being counted does not exist as a session yet.
//
// # Freshness window
//
// Claude Code batches transcript appends via a setTimeout drain at
// FLUSH_INTERVAL_MS (100 ms default, 10 ms under --remote-control,
// sessionStorage.ts:567). The probe resumes from disk. When a live
// Claude process is actively processing a turn, the probe may lag the
// in-memory state by at most the flush interval. For compact
// workflows, where the user pauses chat and switches to a terminal,
// that window is irrelevant. Documented here so future auditors
// understand the one place the probe can diverge from a live /context.
//
// # Cache
//
// Two tiers: an in-process sync.Map with 30s TTL for rapid iterations
// inside one CLI invocation, and a disk cache at
// $XDG_STATE_HOME/clyde/sessions/<id>/context.json with 5 min TTL for
// repeat CLI launches. Both tiers stat the transcript file and reject
// cached entries when transcript mtime has advanced past the capture
// time. The --refresh flag on the compact command busts both tiers.
//
// # Probe side effects
//
// --no-session-persistence suppresses every transcript write through
// sessionStorage.ts shouldSkipPersistence(). Control responses are
// written to stdout only via structuredIO.ts writeToStdout(). Control
// requests bypass the user-message loop, so ~/.claude/history.jsonl is
// not appended. The SessionStart:resume hook fires and invokes clyde's
// own `clyde hook sessionstart`; that hook only updates metadata
// lastAccessed and never writes to the transcript.
package contextusage
