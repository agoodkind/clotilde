package webapp

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

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
