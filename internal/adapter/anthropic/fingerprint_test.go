package anthropic

import (
	"strings"
	"testing"
)

func TestComputeFingerprint(t *testing.T) {
	// Known vectors generated from the Python oracle (/tmp/fp_oracle.py)
	// which mirrors the CLI's TS implementation. The first vector is the
	// CLI capture from 2026-04-19: the prompt produced
	// `cc_version=2.1.114.d29` over the wire, and the algorithm above
	// recomputes "d29" from the same first user message.
	bigPrompt := "Repeat back the word ok. Context: " + strings.Repeat("Lorem ipsum dolor sit amet, consectetur adipiscing elit. ", 600)

	cases := []struct {
		name    string
		message string
		version string
		want    string
	}{
		{"cli_capture_v2.1.114", bigPrompt, "2.1.114", "d29"},
		{"hello", "hello", "2.1.114", "7be"},
		{"empty", "", "2.1.114", "069"},
		{"short_5_chars", "12345", "2.1.114", "27e"},
		{"short_prompt", "Repeat back the word ok.", "2.1.114", "d29"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := computeFingerprint(tc.message, tc.version)
			if got != tc.want {
				t.Fatalf("fingerprint(%q, %q) = %q want %q", truncateForLog(tc.message), tc.version, got, tc.want)
			}
		})
	}
}

func TestVersionFromUserAgent(t *testing.T) {
	cases := []struct {
		name string
		ua   string
		want string
	}{
		{"canonical", "REDACTED-UA", "2.1.114"},
		{"no_paren", "REDACTED-UA", "2.1.114"},
		{"with_semi", "REDACTED-UA; foo", "2.1.114"},
		{"missing_prefix", "Mozilla/5.0", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VersionFromUserAgent(tc.ua)
			if got != tc.want {
				t.Fatalf("VersionFromUserAgent(%q) = %q want %q", tc.ua, got, tc.want)
			}
		})
	}
}

func TestBuildAttributionHeader(t *testing.T) {
	got := BuildAttributionHeader("Repeat back the word ok.", "2.1.114", "sdk-cli")
	want := "x-anthropic-billing-header: cc_version=2.1.114.d29; cc_entrypoint=sdk-cli;"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func truncateForLog(s string) string {
	if len(s) <= 40 {
		return s
	}
	return s[:40] + "..."
}
