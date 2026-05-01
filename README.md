# Clyde

Clyde is a thick wrapper around Claude Code and Codex.

It keeps human-readable session names, adds append-only compaction, and
provides a daemon-backed dashboard for managing Claude sessions without
patching the Claude binary.

## Current Surface

The current CLI surface is intentionally small:

- `clyde` opens the TUI dashboard when stdin and stdout are TTYs.
- `clyde resume <name|uuid>` resolves a Clyde session name to its Claude
  session UUID and runs `claude --resume <uuid>`.
- `clyde compact ...` performs append-only transcript compaction.
- `clyde daemon` runs the background daemon used by the dashboard,
  adapter, OAuth refresh, and pruning loops.
- `clyde hook sessionstart` is the SessionStart hook entrypoint used by
  Claude Code.
- `clyde mcp` runs the MCP stdio server for session search, list, and
  context lookups.

`clyde -r <name>` and `clyde --resume <name>` are rewritten to
`clyde resume <name>`.

Unknown commands are forwarded to the real `claude` binary. This keeps
Claude-native workflows available without Clyde re-implementing them.

## Why Clyde Exists

- Clyde keeps a stable name to UUID mapping under `.claude/clyde/` so you
  can resume work by a human-readable session name.
- Clyde stores per-session `settings.json` files and reuses them on
  resume, which avoids cross-session settings leakage from Claude's global
  settings file.
- Clyde adds append-only compaction, so compaction can preserve original
  transcript lines on disk while injecting a synthetic recap boundary.
- Clyde provides a dashboard and daemon for rename, delete, transcript
  view, remote control, sidecar tail/send, and related session
  management.
- Clyde exposes an MCP server so Claude can search and inspect session
  data in chat.

## Installation

### Download Binary

```bash
# Linux (amd64)
curl -fsSL https://goodkind.io/clyde/releases/latest/download/clyde_linux_amd64.tar.gz | tar xz -C ~/.local/bin

# Linux (arm64)
curl -fsSL https://goodkind.io/clyde/releases/latest/download/clyde_linux_arm64.tar.gz | tar xz -C ~/.local/bin

# macOS (Apple Silicon)
curl -fsSL https://goodkind.io/clyde/releases/latest/download/clyde_darwin_arm64.tar.gz | tar xz -C ~/.local/bin

# macOS (Intel)
curl -fsSL https://goodkind.io/clyde/releases/latest/download/clyde_darwin_amd64.tar.gz | tar xz -C ~/.local/bin
```

### `mise`

```bash
mise use github:agoodkind/clyde
```

### `go install`

```bash
go install goodkind.io/clyde@latest
```

### Build From Source

```bash
git clone https://goodkind.io/clyde
cd clyde
make build
make install  # installs the signed clyde binary to ~/.local/bin/clyde
```

`make install` is a development convenience target. Release tarballs
still install a standalone binary by extraction and do not depend on a
repo checkout.

## Quick Start

### 1. Register the SessionStart Hook

```bash
make install-hook
```

This adds `clyde hook sessionstart` to Claude Code's `SessionStart` hook
configuration in `~/.claude/settings.json`.

### 2. Create or Continue Sessions in Claude

Create new work in Claude itself, for example:

```bash
claude -n auth-feature
```

Or resume an existing session directly in Claude if that is already part
of your workflow.

### 3. Open the Dashboard

```bash
clyde
```

The dashboard is the main user-facing surface. It is a read-mostly TUI
for browsing sessions and driving daemon-backed actions such as resume,
rename, delete, transcript viewing, remote-control toggle, bridge
listing, and sidecar tail/send.

### 4. Resume by Name

```bash
clyde resume auth-feature
```

If Clyde cannot resolve the name in its own store, it forwards the raw
argument to Claude so Claude-native sessions still work.

## Session Layout

Each named session lives under `.claude/clyde/sessions/<name>/`.

Typical files:

- `metadata.json` stores the Clyde session name, current Claude session
  UUID, transcript path, timestamps, and cleanup metadata such as
  `previousSessionIds`.
- `settings.json` stores the per-session Claude settings that Clyde
  passes back to Claude on resume.

Project-scoped Clyde data lives under `.claude/clyde/`. Add that path to
your `.gitignore`.

## Hooks and Lifecycle

`make install-hook` registers a single SessionStart hook:

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "clyde hook sessionstart"
          }
        ]
      }
    ]
  }
}
```

The hook handles:

- new session startup
- resume flows
- `/clear` session UUID rotation
- defensive `/compact` UUID updates if Claude changes that behavior later

The hook is also what injects stored session context into Claude when a
session has context in metadata.

## Commands

### `clyde`

Opens the TUI dashboard when stdin and stdout are TTYs.

When stdin is not a TTY, Clyde forwards to the real `claude` binary
instead of trying to draw the dashboard into a pipe.

### `clyde resume <name|uuid>`

Resolves a Clyde-managed session by name, UUID, display name, or fuzzy
match, then shells out to `claude --resume <uuid>`.

Examples:

```bash
clyde resume auth-feature
clyde --resume auth-feature
clyde -r auth-feature
```

### `clyde compact <session> [target]`

Runs append-only compaction against a session transcript.

Examples:

```bash
clyde compact my-session
clyde compact my-session --tools --thinking
clyde compact my-session 200k
clyde compact my-session --apply
clyde compact my-session --undo
```

By default, `compact` previews changes. Use `--apply` to mutate the
transcript.

### `clyde daemon`

Starts the background daemon. This command is primarily for managed or
internal use.

Useful subcommand:

```bash
clyde daemon reload
```

### `clyde hook sessionstart`

Internal SessionStart hook entrypoint. Claude invokes this through the
hook configuration installed by `make install-hook`.

### `clyde mcp`

Internal MCP stdio server entrypoint used for in-chat session search,
list, and context lookup.

## Remote Control

Remote control is exposed through the dashboard rather than a standalone
CLI verb.

The feature is built from three pieces:

- the Claude wrapper can run Claude inside a PTY and accept daemon-fed
  input over a per-session Unix socket
- the daemon watches bridge state, tails transcripts, and forwards
  messages into the running session
- the dashboard shows RC status, bridge actions, and the Sidecar tab for
  transcript tail and send

## Development

Requirements: Go 1.25+, Make

```bash
make build         # compile and signing-check without leaving a repo-local clyde binary
make test          # run tests
make test-watch    # tests in watch mode
make coverage      # coverage report
make fmt           # format code
make lint          # run linter
make deadcode      # check for unreachable functions
make install       # copy the signed binary to ~/.local/bin/clyde
```

Recommended setup:

```bash
make setup-hooks
make install-hook
```

If you want the managed macOS daemon LaunchAgent:

```bash
make install-launch-agent
```

On macOS, grant Full Disk Access to the stable installed binary path:

```text
~/.local/bin/clyde
```

The LaunchAgent runs that installed binary directly, so rebuilding and
reinstalling keeps the same path and code-signing identity.

`make lint` expects `golangci-lint` v2.x, for example:

```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b "$(go env GOPATH)/bin"
```

## License

This project is licensed under the MIT License. See `LICENSE`.
