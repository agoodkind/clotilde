package webapp

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"goodkind.io/clyde/internal/config"
)

func newTestServer(t *testing.T, cfg config.WebAppConfig, deps Deps) *httptest.Server {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(cfg, deps, log)
	return httptest.NewServer(s.routes())
}

func TestHealthOK(t *testing.T) {
	ts := newTestServer(t, config.WebAppConfig{}, Deps{})
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}

func TestStartOnListenerServesHealth(t *testing.T) {
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := New(config.WebAppConfig{}, Deps{}, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.StartOnListener(ctx, lis) }()

	resp, err := http.Get("http://" + lis.Addr().String() + "/healthz")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server exit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("server did not stop")
	}
}

func TestIndexRendersHTML(t *testing.T) {
	ts := newTestServer(t, config.WebAppConfig{}, Deps{})
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "clyde remote") {
		t.Fatalf("missing title in body")
	}
}

func TestBridgesEndpointSerializesDeps(t *testing.T) {
	deps := Deps{
		Bridges: func() []Bridge {
			return []Bridge{{SessionName: "n", URL: "https://example", PID: 99}}
		},
	}
	ts := newTestServer(t, config.WebAppConfig{}, deps)
	defer ts.Close()
	resp, err := http.Get(ts.URL + "/api/bridges")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got []Bridge
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].URL != "https://example" {
		t.Fatalf("unexpected payload: %+v", got)
	}
}

func TestTokenAuthEnforced(t *testing.T) {
	os.Unsetenv("CLYDE_WEBAPP_TOKEN")
	ts := newTestServer(t, config.WebAppConfig{RequireToken: "secret"}, Deps{})
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/bridges")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Fatalf("no token status = %d, want 401", resp.StatusCode)
	}

	req, _ := http.NewRequest("GET", ts.URL+"/api/bridges", nil)
	req.Header.Set("Authorization", "Bearer secret")
	r2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if r2.StatusCode != 200 {
		t.Fatalf("with token = %d, want 200", r2.StatusCode)
	}
}

func TestStartSessionUsesDaemonLaunch(t *testing.T) {
	var gotName, gotBasedir string
	ts := newTestServer(t, config.WebAppConfig{}, Deps{
		StartRemoteSession: func(_ context.Context, name, basedir string) (string, string, error) {
			gotName = name
			gotBasedir = basedir
			return "chat-demo", "uuid-demo", nil
		},
	})
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/sessions", "application/json", strings.NewReader(`{"name":"demo","basedir":"/tmp/demo"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if gotName != "demo" || gotBasedir != "/tmp/demo" {
		t.Fatalf("launch args = (%q, %q)", gotName, gotBasedir)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "chat-demo" {
		t.Fatalf("name = %v, want chat-demo", body["name"])
	}
	if body["session_id"] != "uuid-demo" {
		t.Fatalf("session_id = %v, want uuid-demo", body["session_id"])
	}
}
