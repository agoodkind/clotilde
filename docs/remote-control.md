# Remote Control

Clotilde can launch Claude Code with the `--remote-control` flag and
expose the running session via `https://claude.ai/code/<bridge>`. Once
the bridge is open, clotilde tracks it in its registry, surfaces the
URL across the dashboard, and offers a Sidecar tab that streams the
live transcript and lets you inject messages back into claude without
leaving the terminal.

## Quick start

```sh
clotilde start my-session --remote-control
```

After claude starts, run `clotilde bridge ls` in another terminal:

```
SESSION                               PID   BRIDGE                            URL
abcd1234-...                          7773  session_01HABC...                 https://claude.ai/code/session_01HABC...
```

Open the URL in any browser, or:

```sh
clotilde bridge open my-session
```

## Per session toggle

Three places drive the per session preference:

- `clotilde start|resume|fork|incognito --remote-control` (or
  `--no-remote-control` to force off) writes the value into the
  session's `settings.json`.
- The dashboard options popup (Enter on a row) shows
  "Enable remote control" / "Disable remote control".
- A profile in `config.toml` can pre-set the value:

  ```toml
  [profiles.qa]
  model = "sonnet"
  remote_control = true
  ```

The `clotilde bridge open` and `clotilde bridge ls` commands are
read-only views of whatever the daemon currently sees.

## Sidecar tab

Press `S` on a Sessions row to pin that session in the Sidecar tab,
or press `3` to switch to the tab and pick later.

The sidecar:

- streams new transcript lines from the daemon's `TailTranscript` RPC,
- shows the bridge URL in the header,
- accepts text in the bottom input that goes straight into claude's
  pty stdin via the daemon's `SendToSession` RPC.

`tab` cycles focus between the body and the input. `enter` sends.
`esc` returns to the Sessions tab. `End` resumes auto-follow when the
buffer scrolls.

## Architecture

Three layers cooperate:

1. The wrapper. When `RemoteControl` is on, `claude.invokeInteractive`
   routes through `invokeInteractivePTY`. Claude runs inside a pty so
   clotilde can multiplex stdin from the local terminal and from a
   per-session Unix socket.
2. The daemon. Owns three relevant pieces:
   - `internal/bridge.Watcher` watches `~/.claude/sessions/` and tracks
     bridge open / close. Events fan out via the existing registry
     stream as `BRIDGE_OPENED` / `BRIDGE_CLOSED`.
   - `internal/transcript.Tailer` plus the `transcriptHub` aggregator
     stream JSONL lines as they're appended. One tailer per active
     transcript regardless of subscriber count.
   - `SendToSession` resolves a session UUID to its inject socket and
     forwards bytes.
3. The TUI. Subscribes once to the registry stream and once to the
   transcript stream of any pinned sidecar session. State lives in
   `App.bridges` (a sync.RWMutex map) and the optional `App.sidecar`
   panel.

## Inject socket

Per session sockets live at:

- `$XDG_RUNTIME_DIR/clotilde/inject/<sessionId>.sock` (preferred), or
- `$TMPDIR/clotilde-inject/<sessionId>.sock` (fallback when
  `XDG_RUNTIME_DIR` is unset, e.g. macOS).

The wrapper opens the socket as a Unix listener. The daemon dials it
on every `SendToSession` call. Bytes flow into the pty as if the user
typed them locally.

## CLI summary

```
clotilde start <name>     --remote-control     enable on launch
clotilde resume <name>    --remote-control     enable when resuming
clotilde fork <name>      --remote-control     enable on fork
clotilde incognito <name> --remote-control     enable for ephemeral session
clotilde bridge ls                             list active bridges
clotilde bridge open <name>                    open bridge URL in $BROWSER
clotilde send <name> "text"                    inject text into a session
```
