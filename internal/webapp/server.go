// Package webapp implements the optional remote dashboard mounted by
// the clyde daemon. The dashboard renders a single HTML page that
// lists every active bridge URL plus a small form for starting a new
// remote control session. Authentication is a static bearer token so
// the listener can sit behind a Cloudflare tunnel without exposing a
// public surface unguarded.
package webapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"goodkind.io/clyde/internal/config"
)

// DefaultPort is the loopback port the dashboard listens on when no
// port is configured. The choice of 11435 avoids the OpenAI adapter
// default at 11434.
const DefaultPort = 11435

// DefaultHost is the loopback bind. The dashboard never binds a
// public interface unless the user explicitly sets WebAppConfig.Host.
const DefaultHost = "[::1]"

// Bridge is the daemon side view of one active remote control
// session. The webapp does not import the daemon protobuf, so the
// caller adapts Bridge entries before passing them in.
type Bridge struct {
	SessionName     string
	SessionID       string
	BridgeSessionID string
	URL             string
	PID             int64
}

// BridgeSource returns the current set of active bridges. The daemon
// supplies an implementation that snapshots its bridge map.
type BridgeSource func() []Bridge

// Deps wires the daemon helpers the webapp needs. ResolveClaudeBin
// is the path used to spawn new sessions; CloseFn is called when
// Start exits so callers can release resources.
type Deps struct {
	Bridges            BridgeSource
	StartRemoteSession func(ctx context.Context, name, basedir string) (sessionName, sessionID string, err error)
}

// Server is the HTTP facade for the dashboard.
type Server struct {
	cfg   config.WebAppConfig
	deps  Deps
	log   *slog.Logger
	token string
	mux   *http.ServeMux
	srv   *http.Server

	mu       sync.Mutex
	starting []startedSession
}

type startedSession struct {
	Name      string
	StartedAt time.Time
	Cmd       string
	Note      string
}

// New builds a Server from the given config plus deps.
func New(cfg config.WebAppConfig, deps Deps, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	token := cfg.RequireToken
	if v := os.Getenv("CLYDE_WEBAPP_TOKEN"); v != "" {
		token = v
	}
	s := &Server{cfg: cfg, deps: deps, log: log.With("component", "webapp"), token: token}
	s.mux = s.routes()
	return s
}

// Addr returns the host:port pair the server will bind.
func (s *Server) Addr() string {
	host := s.cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := s.cfg.Port
	if port <= 0 {
		port = DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// Start binds the listener and serves until ctx is done.
func (s *Server) Start(ctx context.Context) error {
	addr := s.Addr()
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("webapp listen %s: %w", addr, err)
	}
	return s.StartOnListener(ctx, lis)
}

// StartOnListener serves the dashboard on an already-bound listener.
// Daemon reload uses this to inherit the existing dashboard socket
// without creating a bind gap.
func (s *Server) StartOnListener(ctx context.Context, lis net.Listener) error {
	srv := &http.Server{Addr: lis.Addr().String(), Handler: s.mux, ReadHeaderTimeout: 5 * time.Second}
	s.srv = srv
	s.log.Info("webapp listening", "addr", lis.Addr().String())
	errCh := make(chan error, 1)
	go func() { errCh <- srv.Serve(lis) }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// Shutdown stops accepting new dashboard requests, closes idle
// keepalive connections, and lets active handlers finish until ctx
// expires.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	s.srv.SetKeepAlivesEnabled(false)
	return s.srv.Shutdown(ctx)
}

func (s *Server) routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.auth(s.handleIndex))
	mux.HandleFunc("/api/bridges", s.auth(s.handleListBridges))
	mux.HandleFunc("/api/sessions", s.auth(s.handleStartSession))
	mux.HandleFunc("/healthz", s.handleHealth)
	return mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (s *Server) handleListBridges(w http.ResponseWriter, r *http.Request) {
	if s.deps.Bridges == nil {
		writeJSON(w, http.StatusOK, []Bridge{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Bridges())
}

type startSessionRequest struct {
	Name    string `json:"name"`
	Basedir string `json:"basedir"`
	Model   string `json:"model"`
	Effort  string `json:"effort"`
}

func (s *Server) handleStartSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body startSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.deps.StartRemoteSession == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "remote launch unavailable"})
		return
	}
	sessionName, sessionID, err := s.deps.StartRemoteSession(r.Context(), body.Name, body.Basedir)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	s.starting = append(s.starting, startedSession{
		Name:      sessionName,
		StartedAt: time.Now(),
		Cmd:       "daemon StartRemoteSession",
		Note:      "spawned, waiting for bridge URL to appear",
	})
	if len(s.starting) > 50 {
		s.starting = s.starting[len(s.starting)-50:]
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusAccepted, map[string]any{
		"name":       sessionName,
		"session_id": sessionID,
		"cmd":        "daemon StartRemoteSession",
		"note":       "session is starting; refresh /api/bridges in a few seconds",
	})
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" {
			next(w, r)
			return
		}
		want := "Bearer " + s.token
		if r.Header.Get("Authorization") != want {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// indexHTML is the dashboard's single page. It renders a list of
// active bridges and a form for spawning a new remote control
// session. JavaScript polls the bridges endpoint every few seconds
// so a freshly spawned session appears without a manual refresh.
const indexHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>clyde remote</title>
<style>
body{font-family:ui-monospace,SFMono-Regular,Menlo,monospace;background:#0f1115;color:#d8dde6;margin:0;padding:24px;}
h1,h2{font-weight:600;color:#e9eef7;}
a{color:#7cb7ff;}
form,table{background:#161a22;border:1px solid #232936;border-radius:8px;padding:16px;margin:0 0 24px;}
table{width:100%;border-collapse:collapse;}
th,td{text-align:left;padding:8px 10px;border-bottom:1px solid #232936;font-size:13px;}
th{color:#9aa6b8;font-weight:500;}
input,select,button{background:#0f1115;color:#e9eef7;border:1px solid #2c3344;border-radius:6px;padding:8px 10px;margin:4px 6px 4px 0;}
button{cursor:pointer;background:#3b82f6;border-color:#3b82f6;}
button:hover{background:#5fa0ff;}
.empty{color:#9aa6b8;font-style:italic;}
.note{color:#9aa6b8;font-size:12px;margin-top:6px;}
</style></head><body>
<h1>clyde remote</h1>
<h2>Start a new remote control session</h2>
<form id="newform">
  <input name="name" placeholder="session name (optional)">
  <input name="basedir" placeholder="basedir (optional, defaults to home)">
  <select name="model">
    <option value="">model: default</option>
    <option value="claude-4-7-high">claude 4 7 high</option>
    <option value="sonnet">sonnet</option>
    <option value="haiku">haiku</option>
  </select>
  <select name="effort">
    <option value="">effort: default</option>
    <option value="low">low</option>
    <option value="medium">medium</option>
    <option value="high">high</option>
    <option value="max-thinking">max thinking</option>
  </select>
  <button type="submit">Start</button>
  <div class="note">Spawns <code>clyde start --remote-control</code> on the host. The new bridge URL appears below within a few seconds.</div>
</form>

<h2 id="pending-h2" style="display:none">Spawning</h2>
<div id="pending"></div>

<h2>Active bridges</h2>
<table id="bridges"><thead>
<tr><th>session</th><th>URL</th><th>pid</th></tr>
</thead><tbody><tr><td colspan="3" class="empty">loading...</td></tr></tbody></table>

<script>
const pending = []; // {name, startedAt, attempts, cmd}
let lastBridges = [];

async function refresh(){
  try{
    const r = await fetch('/api/bridges');
    if(!r.ok){throw new Error(r.status);}
    lastBridges = await r.json();
    const tb = document.querySelector('#bridges tbody');
    if(!lastBridges.length){tb.innerHTML='<tr><td colspan="3" class="empty">no active bridges</td></tr>';}
    else{
      tb.innerHTML = lastBridges.map(b=>` + "`" + `<tr><td>${escape(b.SessionName)}</td><td><a href="${escape(b.URL)}" target="_blank">${escape(b.URL)}</a></td><td>${b.PID}</td></tr>` + "`" + `).join('');
    }
  }catch(e){
    document.querySelector('#bridges tbody').innerHTML = '<tr><td colspan="3" class="empty">error: '+e+'</td></tr>';
  }
  // Mark pending entries that now have a live bridge.
  for(let i=pending.length-1;i>=0;i--){
    const p = pending[i];
    p.attempts++;
    const matched = lastBridges.find(b=>b.SessionName===p.name || (!p.name && b.PID && (Date.now()-p.startedAt) > 1000));
    if(matched){pending.splice(i,1);}
  }
  renderPending();
}

function renderPending(){
  const h2 = document.getElementById('pending-h2');
  const div = document.getElementById('pending');
  if(!pending.length){h2.style.display='none'; div.innerHTML=''; return;}
  h2.style.display='';
  div.innerHTML = pending.map(p=>{
    const age = Math.round((Date.now()-p.startedAt)/1000);
    return ` + "`" + `<div class="note" style="background:#161a22;border:1px solid #232936;border-radius:6px;padding:8px 12px;margin:6px 0">⏳ ${escape(p.name||'(auto-named)')}: probe ${p.attempts}, ${age}s elapsed<br><code style="font-size:11px">${escape(p.cmd||'')}</code></div>` + "`" + `;
  }).join('');
}

function escape(s){return (s||'').replace(/[&<>"']/g, c=>({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));}

document.getElementById('newform').addEventListener('submit', async ev=>{
  ev.preventDefault();
  const fd = new FormData(ev.target);
  const body = Object.fromEntries(fd.entries());
  const r = await fetch('/api/sessions', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
  const j = await r.json().catch(()=>({}));
  if(!r.ok){alert('Failed: '+JSON.stringify(j)); return;}
  pending.push({name: body.name||'', startedAt: Date.now(), attempts: 0, cmd: j.cmd||('clyde start --remote-control '+(body.name||''))});
  renderPending();
  setTimeout(refresh, 1500);
});

// Poll faster while sessions are pending so the bridge URL appears
// quickly after spawn. Drop to a slower cadence once everything has
// settled to keep idle CPU low.
function tickerInterval(){return pending.length? 1500 : 4000;}
function loop(){refresh().then(()=>setTimeout(loop, tickerInterval()));}
loop();
</script>
</body></html>`
