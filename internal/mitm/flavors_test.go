package mitm

import (
	"strings"
	"testing"
)

func TestClassifyRecordExtractsUserAgentAndBeta(t *testing.T) {
	rec := CaptureRecord{
		Kind: RecordHTTPRequest,
		RequestHeaders: map[string]string{
			"User-Agent":       "claude-cli/2.1.121 (external, cli)",
			"Anthropic-Beta":   "oauth-2025-04-20,interleaved-thinking-2025-05-14" //gitleaks:allow,
			"Anthropic-Version": "2023-06-01",
		},
	}
	sig := ClassifyRecord(rec)
	if !strings.Contains(sig.UserAgent, "claude-cli") {
		t.Errorf("user-agent not extracted: %q", sig.UserAgent)
	}
	if !strings.Contains(sig.BetaFingerprint, "interleaved-thinking") {
		t.Errorf("beta not in fingerprint: %q", sig.BetaFingerprint)
	}
}

func TestBetaFingerprintIsOrderInsensitive(t *testing.T) {
	a := betaFingerprint("oauth-2025-04-20,interleaved-thinking-2025-05-14" //gitleaks:allow)
	b := betaFingerprint("interleaved-thinking-2025-05-14,oauth-2025-04-20")
	if a != b {
		t.Errorf("order-insensitive fingerprint failed: %q != %q", a, b)
	}
}

func TestFlavorSlugStableAndDistinct(t *testing.T) {
	probe := FlavorSignature{
		UserAgent:       "claude-cli/2.1.121 (external, cli)",
		BetaFingerprint: "oauth-2025-04-20,interleaved-thinking-2025-05-14" //gitleaks:allow,
		BodyKeys:        []string{"max_tokens", "messages", "metadata", "model"},
	}
	interactive := FlavorSignature{
		UserAgent:       "claude-cli/2.1.121 (external, cli)",
		BetaFingerprint: "oauth-2025-04-20,interleaved-thinking-2025-05-14,claude-code-20250219,context-1m-2025-08-07", //gitleaks:allow
		BodyKeys:        []string{"max_tokens", "messages", "metadata", "model", "system", "thinking", "tools"},
	}

	probeSlug := probe.FlavorSlug()
	interactiveSlug := interactive.FlavorSlug()

	if probeSlug == interactiveSlug {
		t.Errorf("probe and interactive should have distinct slugs, both got %q", probeSlug)
	}
	if !strings.HasPrefix(probeSlug, "claude-code-probe") {
		t.Errorf("probe slug should be human-readable, got %q", probeSlug)
	}
	if !strings.HasPrefix(interactiveSlug, "claude-code-interactive") {
		t.Errorf("interactive slug should be human-readable, got %q", interactiveSlug)
	}

	// Stable: same signature → same slug across calls.
	if probeSlug != probe.FlavorSlug() {
		t.Errorf("slug not stable: %q vs %q", probeSlug, probe.FlavorSlug())
	}
}

func TestGroupByFlavorPartitionsRequests(t *testing.T) {
	probe := CaptureRecord{
		Kind: RecordHTTPRequest,
		RequestHeaders: map[string]string{
			"User-Agent":     "claude-cli/2.1.121 (external, cli)",
			"Anthropic-Beta": "oauth-2025-04-20",
		},
	}
	interactive := CaptureRecord{
		Kind: RecordHTTPRequest,
		RequestHeaders: map[string]string{
			"User-Agent":     "claude-cli/2.1.121 (external, cli)",
			"Anthropic-Beta": "oauth-2025-04-20,claude-code-20250219",
		},
	}

	groups := GroupByFlavor([]CaptureRecord{probe, interactive, probe})
	if len(groups) < 2 {
		t.Fatalf("expected at least 2 flavor groups, got %d", len(groups))
	}
	totalRequests := 0
	for _, recs := range groups {
		for _, r := range recs {
			if r.Kind == RecordHTTPRequest {
				totalRequests++
			}
		}
	}
	if totalRequests != 3 {
		t.Errorf("expected 3 total requests across groups, got %d", totalRequests)
	}
}

func TestFlavorSlugForCurl(t *testing.T) {
	sig := FlavorSignature{UserAgent: "curl/8.7.1", BodyKeys: []string{}}
	slug := sig.FlavorSlug()
	if !strings.HasPrefix(slug, "curl") {
		t.Errorf("curl slug should start with 'curl', got %q", slug)
	}
}

func TestFlavorSlugForUnknownUA(t *testing.T) {
	sig := FlavorSignature{UserAgent: "", BodyKeys: nil}
	slug := sig.FlavorSlug()
	if !strings.HasPrefix(slug, "unknown") {
		t.Errorf("empty UA should yield 'unknown' prefix, got %q", slug)
	}
}
