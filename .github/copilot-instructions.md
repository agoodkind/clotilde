# Copilot Instructions for clotilde

Clotilde is a Go CLI wrapper around Claude Code that adds named session
management. It wraps Claude Code session UUIDs with human-friendly names,
enabling easy switching between multiple parallel conversations.

## Architecture

```
cmd/                    -> Cobra command implementations
internal/
  session/              -> Session data structures, storage (FileStore), validation
  config/               -> Config management, path resolution
  claude/               -> Claude CLI invocation, path conversion, hook generation
  export/               -> Session transcript export to self-contained HTML
  outputstyle/          -> Output style management
  ui/                   -> TUI components (dashboard, picker, table, confirm)
  util/                 -> UUID generation, filesystem helpers
  testutil/             -> Test utilities (fake claude binary)
```

All packages are under `internal/`; this is a binary, not a library.

## Key Design Decisions

- Thin, non-invasive wrapper. Never modifies Claude Code itself.
- Session data stored in `.claude/clotilde/sessions/<name>/metadata.json`.
- Invokes `claude` CLI with mapped UUIDs via `--session-id` / `--resume`.
- Global hooks in `~/.claude/settings.json` (installed by `clotilde setup`).
- Lazy directory creation: session-creating commands auto-create
  `.claude/clotilde/sessions/` on first use.
- Session-reading commands return friendly messages when no sessions exist.
- Double-hook execution guard via `CLOTILDE_HOOK_EXECUTED` env var prevents
  duplicate output when both global and per-project hooks exist.

## Session Hooks

A single `SessionStart` hook (`clotilde hook sessionstart`) handles all
lifecycle events (startup, resume, compact, clear) based on the `source`
field in JSON input from Claude Code. Fork registration, session ID updates,
and context injection all happen through this hook.

## Conventions

- Go module: `github.com/fgrehm/clotilde`
- Test framework: Ginkgo/Gomega
- Linting: golangci-lint v2, formatting: gofumpt (via `golangci-lint fmt`)
- Commit format: Conventional Commits, present tense, under 72 chars
- `CHANGELOG.md` uses Keep a Changelog format
