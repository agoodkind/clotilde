// Claude Code attribution-header fingerprint reproduction.
//
// Ported from
// clyde-research/claude-code-TYPESCRIPT-SRC/restored-src/src/utils/fingerprint.ts
// (CLI v2.1.114). The CLI computes a 3-char SHA256-derived fingerprint
// over a salt + a 3-char sample of the first user message + the CLI
// version string, and embeds it as the suffix of the
// `x-anthropic-billing-header: cc_version=<version>.<fingerprint>` line
// inside the request system prompt. Anthropic's OAuth backend likely
// validates the suffix matches the recomputed hash for the request body
// it received.
//
// The salt and the sampling indices are hardcoded in the official CLI
// with the explicit warning "Must match exactly for fingerprint
// validation to pass" and "Do not change this method without careful
// coordination with 1P and 3P APIs". Same here.
package anthropic

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VersionFromUserAgent parses the CLI version out of a User-Agent string
// like `REDACTED-UA` and returns the version
// segment (e.g. "2.1.114"). Returns "" if it can't find one. Used by
// callers that want to keep the impersonated CLI version coupled to the
// configured User-Agent (so changing one in the toml doesn't drift from
// the other).
func VersionFromUserAgent(ua string) string {
	const prefix = "claude-cli/"
	idx := strings.Index(ua, prefix)
	if idx < 0 {
		return ""
	}
	rest := ua[idx+len(prefix):]
	if space := strings.IndexAny(rest, " ;("); space >= 0 {
		return rest[:space]
	}
	return rest
}

// fingerprintSalt is the constant from
// claude-code-TYPESCRIPT-SRC/restored-src/src/utils/fingerprint.ts:8.
// Verified by reproducing the captured CLI value `cc_version=2.1.114.d29`
// from the matching prompt: SHA256(salt + "ab " + "2.1.114")[:3] = "d29".
const fingerprintSalt = "REDACTED-SALT"

// fingerprintIndices selects characters from the first user message
// text for the hash input. From the same file: indices [4, 7, 20]
// with "0" substituted when the message is shorter.
var fingerprintIndices = [...]int{4, 7, 20}

// computeFingerprint returns the 3-char hex prefix of
// SHA256(salt + msg[4]+msg[7]+msg[20] + version). messageText is the
// first user message body (string content for plain messages, or the
// concatenated text of the first user message's text parts).
func computeFingerprint(messageText, version string) string {
	chars := make([]byte, len(fingerprintIndices))
	runes := []rune(messageText)
	for k, i := range fingerprintIndices {
		if i < len(runes) {
			// The CLI runs in JS; runes/code units roughly align for
			// ASCII prompts. For non-ASCII first messages the JS and
			// Go indexing can differ; cross that bridge if it surfaces
			// as a 429.
			chars[k] = byte(runes[i])
		} else {
			chars[k] = '0'
		}
	}
	blob := []byte(fingerprintSalt)
	blob = append(blob, chars...)
	blob = append(blob, version...)
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:3]
}

// BuildAttributionHeader returns the body-side
// `x-anthropic-billing-header: cc_version=<version>.<fp>; cc_entrypoint=<entrypoint>;`
// line that the OAuth bucket classifier reads from the request system
// prompt. firstUserText is the first user message body; version is the
// CLI version we are impersonating (e.g. "2.1.114"); entrypoint is the
// `cc_entrypoint` value (e.g. "sdk-cli").
//
// The CLI also conditionally appends ` cch=00000;` (overwritten in
// flight by Bun's attestation stack) and ` cc_workload=<x>;` for
// cron-initiated requests. Neither is implemented here: cch is gated on
// a build-time feature flag we cannot reproduce, and cc_workload is not
// applicable to a daemon-driven OAuth bridge.
func BuildAttributionHeader(firstUserText, version, entrypoint string) string {
	fp := computeFingerprint(firstUserText, version)
	return "x-anthropic-billing-header: cc_version=" + version + "." + fp +
		"; cc_entrypoint=" + entrypoint + ";"
}
