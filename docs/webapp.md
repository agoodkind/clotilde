# Remote dashboard webapp

The clyde daemon optionally hosts a small HTML dashboard. The
dashboard lists every active `--remote-control` bridge URL and
exposes a form for spawning a new remote control session. It runs in
the same process as the gRPC daemon and the OpenAI adapter, so a
single launchd entry on macOS or a single systemd unit on Linux
covers everything.

## Enable

Add a stanza to `~/.config/clyde/config.toml`:

```toml
[web_app]
enabled = true
port = 11435
host = "127.0.0.1"
require_token = "your-bearer-here"   # optional auth
```

Restart the daemon. Visit `http://localhost:11435/`.

## Endpoints

| Method | Path             | Purpose                                              |
|--------|------------------|------------------------------------------------------|
| GET    | `/`              | HTML dashboard                                       |
| GET    | `/api/bridges`   | JSON list of every active bridge                     |
| POST   | `/api/sessions`  | Spawn a new `--remote-control` session via clyde |
| GET    | `/healthz`       | Liveness probe                                       |

POST `/api/sessions` payload:

```json
{
  "name": "my-session",
  "basedir": "/home/me/code/proj",
  "model": "claude-4-7-high",
  "effort": "high"
}
```

Every field is optional. Empty values fall through to the clyde
defaults the same way the CLI flags do.

## Cloudflare tunnel

The dashboard binds loopback by default. Pair it with cloudflared to
expose it through an authenticated tunnel:

```sh
cloudflared tunnel --url http://localhost:11435
```

For named tunnels with Access policies, follow the standard
cloudflared docs and add a Service Token rule on the hostname.

## Caveats

The webapp shells out to the `clyde` binary on the host to spawn
sessions. The path can be set via `web_app.clyde_binary`; without
it the daemon falls back to `clyde` on PATH. Tool authoring,
templates, and a richer multi-pane viewer are out of scope for the
first version. Open `https://claude.ai/code/<bridge-id>` from the
dashboard rows to drive a live session.
