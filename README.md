# Clyde

A power-user companion for Claude Code.

## Why?

Claude Code has gotten better at session management: `-n` names sessions, `/branch` creates forks, and `/resume` shows a picker. But daily use still has friction:

- **Settings leak across sessions**: Claude Code stores model, effort, and permissions in `~/.claude/settings.json`  --  a single global file shared by every running session. Change `/model` in one terminal and every other open session picks it up immediately ([known issue](https://github.com/anthropics/claude-code/issues/20745)). Clyde isolates settings per session using a dedicated `settings.json` for each one, re-applied automatically on every resume. Want opus for deep work and haiku for quick tasks? Both can run at the same time without interfering.
- **No reusable presets**: Clyde profiles let you define named configurations (model, permissions, output style) in a config file and apply them with `--profile <name>`.
- **No persistent context**: Clyde's `--context` flag attaches a note to a session (ticket number, current goal) that's injected into Claude automatically at startup and resume.
- **Every session persists**: Clyde's incognito sessions auto-delete all data  --  metadata, transcripts, logs  --  when you exit.
- **Fork only from inside Claude**: `clyde fork` creates a branched conversation from anywhere on the command line, with parent/child tracking.
- **Common flag combos are a mouthful**: `--fast` means haiku + low effort. `--yolo` means bypass permissions. One flag instead of two or three.
- **No completion for session names**: Clyde adds shell completion for session names and profile names across bash, zsh, and fish.
- **Transcripts are unreadable JSONL**: `clyde export` renders a session as self-contained HTML with syntax highlighting, collapsible thinking blocks, and formatted tool outputs.

## What Clyde does

```bash
# Start and resume named sessions
clyde start auth-feature
clyde resume auth-feature

# Settings stick  --  set them once, re-applied automatically on every resume
clyde start deep-work --model opus --effort high
clyde resume deep-work                             # opus + high effort, no flags needed

# Profiles for reusable configurations
clyde start spike --profile quick                 # haiku + bypass permissions
clyde start review --profile strict               # deny Bash/Write, ask mode

# Session context, injected into Claude at startup
clyde start auth-feature --context "working on GH-123"

# Incognito: auto-deletes everything on exit
clyde incognito

# Fork a session from the command line
clyde fork auth-feature auth-experiment

# Export a session as self-contained HTML
clyde export auth-feature

```

Clyde is a thin wrapper: it maps human-readable names to Claude Code UUIDs, invokes `claude` with the right flags, and never patches or modifies Claude Code itself.

## Installation

**Download binary** (recommended)

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

**mise:**
```bash
mise use github:agoodkind/clyde
```

**Go install:**
```bash
go install goodkind.io/clyde@latest
```

**Build from source:**
```bash
git clone https://goodkind.io/clyde
cd clyde
make build
make install  # installs to ~/.local/bin
```

## Quick Start

```bash
# One-time setup (registers SessionStart hooks globally)
clyde setup

# Start a new named session
clyde start auth-feature

# Resume it later
clyde resume auth-feature

# List all sessions
clyde list

# Inspect a session (settings, context, files)
clyde inspect auth-feature

# Fork a session to try something different
clyde fork auth-feature experiment

# Delete when done
clyde delete experiment
```

## How It Works

Clyde never patches or modifies Claude Code.

- Each session is a folder in `.claude/clyde/sessions/<name>/` containing metadata and optional settings
- `clyde setup` registers a SessionStart hook in `~/.claude/settings.json` that handles context injection and `/clear` UUID tracking
- Claude Code is invoked with `--session-id` (new sessions), `--resume` (existing), and `--settings` (model, effort, permissions)

**Worktrees:** `.claude/clyde/` lives in each worktree's `.claude/` directory, so each worktree gets its own independent sessions. Use worktrees for major branches, Clyde for managing multiple conversations within each.

**Gitignore:** `.claude/clyde/` contains ephemeral, per-user session state  --  add it to your `.gitignore`.

## Features

### Sticky Session Settings

Claude Code stores model, effort, and permissions in `~/.claude/settings.json`  --  a global file shared by every running session. Changing `/model` in one terminal affects all others. Clyde fixes this by giving each session its own `settings.json`, passed via `--settings` on every invocation.

Flags set on `start` and `incognito` are saved to the session and re-applied automatically on every resume. No need to repeat flags.

```bash
# Set once
clyde start deep-work --model opus --effort high

# Resume any number of times  --  settings apply automatically
clyde resume deep-work

# Run two sessions with different models simultaneously  --  they don't interfere
clyde start quick-task --fast      # haiku in one terminal
clyde resume deep-work             # opus in another, unaffected
```

**What gets persisted:** `--model`, `--effort`, `--fast` (stores `model=haiku` + `effortLevel=low`), `--permission-mode`, `--allowed-tools`, `--disallowed-tools`, `--add-dir`, `--output-style`.

Override for a specific resume by passing the flag  --  CLI always wins over stored settings:

```bash
clyde resume deep-work --model sonnet   # one-off override, stored settings unchanged
```

### Session Context

Attach a note to a session so Claude knows what you're working on. Stored in session metadata and injected automatically at every startup:

```bash
clyde start auth-feature --context "working on ticket GH-123"
clyde fork auth-feature experiment --context "trying JWT instead of sessions"

# Update when switching tasks
clyde resume auth-feature --context "now on GH-456"
```

Forked sessions inherit context from the parent unless overridden. `clyde inspect <name>` shows the stored context.

### Incognito Sessions

Incognito sessions auto-delete themselves  --  metadata, transcripts, and agent logs  --  when you exit:

```bash
clyde incognito
clyde incognito quick-test --fast
```

Cleanup runs on normal exit (Ctrl+D, `/exit`). If the process is killed (SIGKILL), the session may persist; use `clyde delete <name>` to clean up manually.

You cannot fork *from* an incognito session, but you can fork *to* one: `clyde fork auth-feature temp --incognito`.

### Forking

Fork creates a new session starting from the parent's conversation history:

```bash
clyde fork auth-feature experiment
clyde fork auth-feature --incognito                          # random name, auto-deletes
clyde fork auth-feature temp --context "trying a different approach"
```

Settings and context are inherited from the parent. The fork gets its own UUID and metadata; the parent is unaffected.

### Profiles

Define named presets in a config file and apply them with `--profile`:

```json
{
  "profiles": {
    "quick": {
      "model": "haiku",
      "permissionMode": "bypassPermissions"
    },
    "strict": {
      "permissions": {
        "deny": ["Bash", "Write"],
        "defaultMode": "ask"
      }
    },
    "research": {
      "model": "sonnet",
      "outputStyle": "Explanatory"
    }
  }
}
```

Place profiles in `~/.config/clyde/config.json` (global, respects `$XDG_CONFIG_HOME`) or `.claude/clyde/config.json` (project-scoped). Project profiles override global ones with the same name.

```bash
clyde start spike --profile quick
clyde start sandboxed --profile strict

# CLI flags override profile values
clyde start research --profile quick --model sonnet
```

**Profile fields:** `model`, `permissionMode`, `permissions` (allow/deny/ask/additionalDirectories/defaultMode/disableBypassPermissionsMode), `outputStyle`.

**Precedence:** global profile → project profile → CLI flags.

### Shorthand Flags

Available on all commands (`start`, `incognito`, `resume`, `fork`):

```bash
# Permission modes
clyde start refactor --accept-edits    # auto-approve edits
clyde incognito --yolo                 # bypass all permission checks
clyde start spike --plan              # plan mode
clyde resume my-session --dont-ask    # approve everything without asking

# Fast mode: haiku + low effort (persisted in session settings on start/incognito)
clyde start quick-check --fast
clyde incognito --fast --yolo
```

`--fast` cannot be combined with `--model` or `--effort`. Permission shortcuts are mutually exclusive with each other and with `--permission-mode`.

### Output Styles

Customize how Claude communicates in a session:

```bash
# Built-in styles (case-sensitive)
clyde start myfeature --output-style Explanatory
clyde start myfeature --output-style Learning

# Custom style from file
clyde start myfeature --output-style-file ./my-style.md

# Inline custom content (creates a session-specific style file)
clyde start myfeature --output-style "Be concise and use bullet points"

# Existing named style (from .claude/output-styles/ or ~/.claude/output-styles/)
clyde start myfeature --output-style my-project-style
```

Session-specific custom styles are stored in `.claude/output-styles/clyde/<session-name>.md` and should be gitignored. Team-shared styles go in `.claude/output-styles/` (committed to git).

### Pass-Through Flags

Pass any Claude Code flag directly using `--`:

```bash
clyde start my-session -- --debug api,hooks
clyde resume my-session -- --verbose
```

Pass-through flags apply to that invocation only and are not persisted. Use named flags (`--model`, `--effort`, etc.) if you want settings to stick across resumes.

## Commands

### `clyde setup [--local]`

One-time setup. Registers a SessionStart hook in `~/.claude/settings.json`.

```bash
clyde setup              # registers hooks globally (recommended)
clyde setup --local      # registers in ~/.claude/settings.local.json instead
```

After setup, `clyde start` works in any project directory.

### `clyde start [name] [options]`

Start a new named session. Auto-generates a name like `2026-03-09-happy-fox` if none is provided.

```bash
clyde start
clyde start my-session
clyde start bugfix --model haiku --effort low
clyde start spike --profile quick
clyde start sandboxed --permission-mode plan --allowed-tools "Read,Bash(npm:*)" --disallowed-tools "Write"
clyde start auth-feature --context "working on GH-123"
```

**Options:**
- `--model <model>`  --  Model (haiku, sonnet, opus). Persisted in session settings.
- `--effort <level>`  --  Reasoning effort (low, medium, high, max). Persisted in session settings.
- `--fast`  --  haiku + low effort. Persisted in session settings.
- `--profile <name>`  --  Named profile (baseline; CLI flags override).
- `--context <text>`  --  Session context, injected at startup.
- `--incognito`  --  Auto-delete session on exit.
- `--accept-edits`  --  Shorthand for `--permission-mode acceptEdits`.
- `--yolo`  --  Shorthand for `--permission-mode bypassPermissions`.
- `--plan`  --  Shorthand for `--permission-mode plan`.
- `--dont-ask`  --  Shorthand for `--permission-mode dontAsk`.
- `--permission-mode <mode>`  --  acceptEdits, bypassPermissions, default, dontAsk, plan. Persisted.
- `--allowed-tools <tools>`  --  Comma-separated allowed tools (e.g. `Bash(npm:*),Read`). Persisted.
- `--disallowed-tools <tools>`  --  Comma-separated denied tools. Persisted.
- `--add-dir <directories>`  --  Additional directories to allow access to. Persisted.
- `--output-style <style>`  --  Built-in name, existing style name, or inline content. Persisted.
- `--output-style-file <path>`  --  Path to custom output style file. Persisted.

### `clyde incognito [name] [options]`

Start an incognito session that auto-deletes on exit. Same options as `clyde start` (`--incognito` is implicit).

```bash
clyde incognito
clyde incognito quick-test
clyde incognito --fast --yolo
```

### `clyde resume [name] [options]`

Resume a session by name. Shows an interactive picker if no name is provided (TTY only). Stored settings from `settings.json` are applied automatically; flags override them for this invocation only.

```bash
clyde resume auth-feature
clyde resume auth-feature --model sonnet        # one-off model override
clyde resume auth-feature --fast                # one-off fast mode
clyde resume auth-feature --accept-edits
clyde resume auth-feature --context "now on GH-456"
```

**Options:**
- `--context <text>`  --  Update the stored session context.
- `--model <model>`  --  Override model for this invocation only.
- `--effort <level>`  --  Override effort level for this invocation only.
- `--accept-edits`, `--yolo`, `--plan`, `--dont-ask`, `--fast`  --  Shorthand flags.

### `clyde fork <parent> [name] [options]`

Fork a session. Inherits settings and context from the parent. If no name is provided with `--incognito`, a random name is generated.

```bash
clyde fork auth-feature auth-experiment
clyde fork auth-feature --incognito
clyde fork auth-feature temp --context "trying different approach"
```

**Options:**
- `--context <text>`  --  Context for the fork (inherits from parent if not specified).
- `--incognito`  --  Fork as incognito session.
- `--accept-edits`, `--yolo`, `--plan`, `--dont-ask`, `--fast`  --  Shorthand flags.

**Note:** Cannot fork *from* incognito sessions; can fork *to* them.

### `clyde list`

List all sessions with name, model, and last used timestamp.

### `clyde inspect <name>`

Show detailed session info: UUID, timestamps, settings, context, associated files, and Claude Code data status.

### `clyde delete <name> [--force]`

Delete a session and all associated Claude Code data (current and previous transcripts, agent logs).

- `--force, -f`  --  Skip confirmation.

### `clyde export <name> [options]`

Export a session as self-contained HTML with syntax-highlighted code, collapsible thinking blocks, and expandable tool outputs.

```bash
clyde export auth-feature
clyde export auth-feature -o ~/Desktop/auth-session.html
clyde export auth-feature --stdout | wc -c
```

**Options:**
- `-o, --output <path>`  --  Output path (default: `./<name>.html`).
- `--stdout`  --  Write to stdout.

**Keyboard shortcuts** in the exported HTML: `Ctrl+T` toggles thinking blocks, `Ctrl+O` toggles tool outputs.

### `clyde` (no subcommand)

Interactive dashboard in TTY: start a new session, resume, fork, list, or delete.

### `clyde completion <shell>`

Generate shell completion scripts for bash, zsh, fish, or powershell. See `clyde completion --help` for setup instructions.

## Related Work

Claude Code now has native session naming (`-n`/`--name`), `/rename`, `/branch`, and a `/resume` picker. Clyde uses these under the hood and focuses on what Claude Code doesn't provide: sticky settings, profiles, context injection, incognito sessions, forking by name, session export, and shorthand flags.

**On `/branch` and `/rename`:** Clyde doesn't detect or track when you use these inside Claude. For clyde-managed forks with parent/child tracking, use `clyde fork`. Sessions created via `/branch` live outside Clyde's tracking.

### Other tools in this space

- [**tweakcc**](https://github.com/Piebald-AI/tweakcc)  --  Patches Claude Code to add custom system prompts, toolsets, themes, and more
- [**claude-code-transcripts**](https://github.com/simonw/claude-code-transcripts)  --  Python tool for converting JSONL transcripts to HTML (Simon Willison)
- [**claude-code-log**](https://github.com/daaain/claude-code-log)  --  Python CLI for transcript viewing with TUI browser
- [**OpCode**](https://github.com/winfunc/opcode)  --  GUI app for managing Claude Code sessions, agents, and background tasks

Clyde differs in being non-invasive (no patching), a single Go binary with no runtime dependencies, and focused on daily ergonomics rather than UI.

## Development

**Requirements:** Go 1.25+, Make

```bash
make build         # build to dist/clyde
make test          # run tests
make test-watch    # tests in watch mode
make coverage      # coverage report
make fmt           # format code
make lint          # run linter
make deadcode      # check for unreachable functions
make install       # install to ~/.local/bin
```

**Git hooks** (recommended  --  runs format and lint on commit):
```bash
make setup-hooks
```

`make lint` requires golangci-lint v2.x to match CI:
```bash
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin
```

## About the Name

Clyde is a short, memorable project name chosen to stay easy to type with plain ASCII.

---

Built with [Claude Code](https://claude.ai/code).
