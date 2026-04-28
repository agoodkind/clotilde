package mitm

import (
	"context"
	"log/slog"
	"strings"

	"goodkind.io/clyde/internal/config"
)

// LaunchHints is the typed bundle the always-on TOML-driven launch
// path returns to its callers. Env carries env-var overrides the
// caller should merge into the spawned process. Args carries
// command-line flags the caller should append (Chromium flags for
// Electron renderers; empty for plain CLI binaries). CACertPath is
// the mitmproxy CA cert path, useful when callers want to set
// SSL_CERT_FILE / NODE_EXTRA_CA_CERTS themselves.
type LaunchHints struct {
	Env         map[string]string
	Args        []string
	CACertPath  string
	ProxyURL    string
	UpstreamKey string
}

// LaunchHintsFor returns the typed launch hints for one upstream
// when MITM is enabled in config. The upstream key is one of the
// LaunchProfile names (e.g. "claude-code", "codex-cli",
// "claude-desktop", "codex-desktop"). Returns the zero value when
// MITM is disabled, or when the upstream is not listed in
// `cfg.Providers`.
//
// The provider name in `cfg.Providers` is matched against the
// LaunchProfile family ("claude" or "codex") so existing TOML
// configs that say `providers = ["claude"]` continue to work and
// pick up both claude-code and claude-desktop hints.
func LaunchHintsFor(ctx context.Context, cfg config.MITMConfig, upstreamKey string, log *slog.Logger) (LaunchHints, error) {
	family := upstreamFamily(upstreamKey)
	if !cfg.EnabledDefault || !cfg.EnabledFor(family) {
		return LaunchHints{}, nil
	}
	proxy, err := EnsureStarted(cfg, log)
	if err != nil {
		return LaunchHints{}, err
	}

	hints := LaunchHints{
		Env:         map[string]string{},
		ProxyURL:    proxy.base,
		UpstreamKey: upstreamKey,
	}
	switch family {
	case "claude":
		hints.Env["ANTHROPIC_BASE_URL"] = proxy.ClaudeBaseURL()
	case "codex":
		// Codex CLI / Desktop respects HTTPS_PROXY; the codex home
		// overlay (PrepareCodexOverlay) wires the base URLs for the
		// CLI. Electron callers route renderer traffic through the
		// proxy via Chromium flags below.
		hints.Env["HTTPS_PROXY"] = proxy.base
		hints.Env["HTTP_PROXY"] = proxy.base
	}

	// Chromium flags for Electron upstreams. The LaunchProfile knows
	// which upstreams are Electron; non-Electron returns nil.
	if profile, err := LookupLaunchProfile(upstreamKey); err == nil {
		hints.Args = profile.ChromiumFlags(proxy.base)
	}

	return hints, nil
}

func upstreamFamily(upstreamKey string) string {
	switch {
	case strings.HasPrefix(upstreamKey, "claude"):
		return "claude"
	case strings.HasPrefix(upstreamKey, "codex"):
		return "codex"
	}
	return upstreamKey
}
