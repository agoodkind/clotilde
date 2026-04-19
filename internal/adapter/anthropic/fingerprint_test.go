package anthropic

import (
	"regexp"
	"strings"
	"testing"
)

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

func TestBuildAttributionHeaderShape(t *testing.T) {
	// Wire shape from mitm: cc_version=<semver>.<3hex>; cc_entrypoint=...; cch=<5hex>;
	re := regexp.MustCompile(`^x-anthropic-billing-header: cc_version=\d+\.\d+\.\d+\.[0-9a-f]{3}; cc_entrypoint=sdk-cli; cch=[0-9a-f]{5};$`)
	for range 20 {
		got := BuildAttributionHeader("2.1.114", "sdk-cli")
		if !re.MatchString(got) {
			t.Fatalf("unexpected shape: %q", got)
		}
	}
}

func TestBuildAttributionHeaderRandomSuffixes(t *testing.T) {
	seen := map[string]struct{}{}
	for range 30 {
		line := BuildAttributionHeader("1.0.0", "sdk-cli")
		// Two calls should usually differ (extremely unlikely collision).
		seen[line] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatalf("expected variation across calls, got %d unique of 30", len(seen))
	}
}

func TestRandomHexDeterministicLength(t *testing.T) {
	for _, n := range []int{1, 2, 3, 4, 5, 6} {
		h := randomHex(n)
		if len(h) != n {
			t.Fatalf("randomHex(%d) len=%d", n, len(h))
		}
		for _, r := range h {
			if !strings.ContainsRune("0123456789abcdef", r) {
				t.Fatalf("non-hex in %q", h)
			}
		}
	}
}
