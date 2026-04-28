package mitm

import (
	"strings"
	"testing"
)

func TestLaunchProfilesCoverSupportedUpstreams(t *testing.T) {
	profiles := LaunchProfiles()
	for _, name := range []string{"codex-cli", "codex-desktop", "claude-code", "claude-desktop", "vscode"} {
		if _, ok := profiles[name]; !ok {
			t.Errorf("missing profile %q", name)
		}
	}
}

func TestLookupLaunchProfileUnknownReturnsHelpfulError(t *testing.T) {
	_, err := LookupLaunchProfile("not-a-real-thing")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "supported:") {
		t.Errorf("error should list supported set, got %q", err.Error())
	}
}

func TestComposeEnvOverridesParent(t *testing.T) {
	p := LaunchProfile{Name: "codex-cli"}
	parent := []string{"PATH=/usr/bin", "HTTPS_PROXY=http://stale:1234", "OTHER=keep"}
	got := p.ComposeEnv(parent, map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:8888",
	})
	hasNew := false
	hasOther := false
	hasStale := false
	for _, kv := range got {
		switch kv {
		case "HTTPS_PROXY=http://127.0.0.1:8888":
			hasNew = true
		case "OTHER=keep":
			hasOther = true
		case "HTTPS_PROXY=http://stale:1234":
			hasStale = true
		}
	}
	if !hasNew {
		t.Errorf("override missing")
	}
	if !hasOther {
		t.Errorf("non-overridden parent var dropped")
	}
	if hasStale {
		t.Errorf("stale parent value not removed")
	}
}

func TestChromiumFlagsOnlyForElectron(t *testing.T) {
	codex := LaunchProfile{Name: "codex-cli"}
	if got := codex.ChromiumFlags("http://127.0.0.1:8888"); len(got) != 0 {
		t.Errorf("non-electron should have no chromium flags, got %v", got)
	}

	desktop := LaunchProfile{Name: "codex-desktop", IsElectron: true}
	flags := desktop.ChromiumFlags("http://127.0.0.1:8888")
	if len(flags) == 0 {
		t.Fatal("electron should have chromium flags")
	}
	hasProxy := false
	hasIgnoreCert := false
	for _, f := range flags {
		if f == "--proxy-server=http://127.0.0.1:8888" {
			hasProxy = true
		}
		if f == "--ignore-certificate-errors" {
			hasIgnoreCert = true
		}
	}
	if !hasProxy {
		t.Errorf("missing --proxy-server, got %v", flags)
	}
	if !hasIgnoreCert {
		t.Errorf("missing --ignore-certificate-errors, got %v", flags)
	}
}
