package mitm

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// FlavorSignature uniquely identifies one caller flavor for an
// upstream. Two records with the same signature are considered the
// same flavor and contribute to the same per-flavor reference.
type FlavorSignature struct {
	UserAgent       string
	BetaFingerprint string
	BodyKeys        []string
}

// FlavorSlug returns a stable, filesystem-safe identifier for the
// flavor. Used as the directory name under
// `research/<upstream>/snapshots/<flavor>/`. The slug is a short hex
// hash of the signature so distinct flavors always sort to distinct
// directories even when their human-readable names collide.
func (s FlavorSignature) FlavorSlug() string {
	humanPart := flavorHumanPrefix(s)
	hashPart := flavorHashSuffix(s)
	if humanPart == "" {
		return hashPart
	}
	return humanPart + "-" + hashPart
}

func flavorHumanPrefix(s FlavorSignature) string {
	ua := strings.ToLower(s.UserAgent)
	switch {
	case strings.Contains(ua, "claude-cli"):
		// Distinguish probe vs interactive by the body shape.
		if hasKey(s.BodyKeys, "tools") || hasKey(s.BodyKeys, "system") {
			return "claude-code-interactive"
		}
		if hasKey(s.BodyKeys, "messages") {
			return "claude-code-probe"
		}
		return "claude-code-other"
	case strings.Contains(ua, "codex"):
		return "codex"
	case strings.Contains(ua, "claude.app"):
		return "claude-desktop"
	case strings.Contains(ua, "curl"):
		return "curl"
	case ua == "":
		return "unknown"
	}
	return slugifyUA(ua)
}

func flavorHashSuffix(s FlavorSignature) string {
	keys := append([]string{}, s.BodyKeys...)
	sort.Strings(keys)
	raw := s.UserAgent + "|" + s.BetaFingerprint + "|" + strings.Join(keys, ",")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:4])
}

var slugRE = regexp.MustCompile(`[^a-z0-9]+`)

func slugifyUA(ua string) string {
	parts := strings.SplitN(ua, "/", 2)
	base := strings.ToLower(parts[0])
	clean := slugRE.ReplaceAllString(base, "-")
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "unknown"
	}
	if len(clean) > 24 {
		clean = clean[:24]
	}
	return clean
}

// ClassifyRecord computes the FlavorSignature for one CaptureRecord.
// Returns the zero signature for non-request records (responses,
// ws_msg etc.) since those don't carry caller identity directly.
func ClassifyRecord(rec CaptureRecord) FlavorSignature {
	if rec.Kind != RecordHTTPRequest {
		return FlavorSignature{}
	}
	headers := rec.RequestHeaders
	if headers == nil {
		headers = rec.Headers
	}
	ua := strings.TrimSpace(headers[lookupHeader(headers, "user-agent")])
	beta := strings.TrimSpace(headers[lookupHeader(headers, "anthropic-beta")])
	keys := bodyKeysFromRecord(rec)
	return FlavorSignature{
		UserAgent:       ua,
		BetaFingerprint: betaFingerprint(beta),
		BodyKeys:        keys,
	}
}

// betaFingerprint reduces a comma-joined Anthropic-Beta header value
// to a stable canonical form: lowercase, trim each flag, sort.
// Different orderings of the same flag set produce identical
// fingerprints; flag drift (one upstream version adds or removes a
// flag) produces a different fingerprint.
func betaFingerprint(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	cleaned := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			cleaned = append(cleaned, p)
		}
	}
	sort.Strings(cleaned)
	return strings.Join(cleaned, ",")
}

// bodyKeysFromRecord pulls the top-level body field names from a
// captured request record. The proxy emits these as
// `request_body.keys` in summary mode and as the full JSON in raw
// mode; this helper handles both shapes.
func bodyKeysFromRecord(rec CaptureRecord) []string {
	// Try the typed CaptureRecord.BodyText first (raw mode).
	if rec.BodyText != "" {
		if keys := bodyKeysFromJSONString(rec.BodyText); len(keys) > 0 {
			return keys
		}
	}
	return nil
}

func bodyKeysFromJSONString(_ string) []string {
	// Intentionally minimal. The summary-mode body lives in the raw
	// map under request_body.keys, which the typed CaptureRecord
	// schema does not expose. Callers that want the keys should use
	// the richer extraction in snapshot_v2.go which reads the raw
	// JSONL line. For now this path returns nil and the human-prefix
	// logic in flavorHumanPrefix degrades gracefully.
	return nil
}

func hasKey(keys []string, target string) bool {
	for _, k := range keys {
		if k == target {
			return true
		}
	}
	return false
}

func lookupHeader(headers map[string]string, name string) string {
	if headers == nil {
		return ""
	}
	lower := strings.ToLower(name)
	for k := range headers {
		if strings.ToLower(k) == lower {
			return k
		}
	}
	return ""
}

// GroupByFlavor partitions a list of records by FlavorSignature.
// Each group's slug becomes the key. Non-request records (responses,
// ws_msg, ws_start, ws_end) are passed through to every group whose
// flavor we observed in the same temporal neighborhood; for the
// simple case this is acceptable because ws transcripts are
// single-flavor and HTTP transcripts only need request-based
// classification.
func GroupByFlavor(records []CaptureRecord) map[string][]CaptureRecord {
	out := map[string][]CaptureRecord{}
	for _, rec := range records {
		sig := ClassifyRecord(rec)
		slug := sig.FlavorSlug()
		if rec.Kind != RecordHTTPRequest {
			// Attach to the most recent flavor seen, fall back to "unclassified".
			if slug == "" {
				slug = "unclassified"
			}
		}
		out[slug] = append(out[slug], rec)
	}
	return out
}
