package compact

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseTokenCount parses a user-supplied token count spec into an int.
// Accepts integers with optional k/m suffixes and comma thousands
// separators. Examples:
//
//	"200k"     -> 200_000
//	"1.5m"     -> 1_500_000
//	"120,000"  -> 120_000
//	"120000"   -> 120_000
//	"0.5m"     -> 500_000
//
// Suffix is case insensitive. Spaces around the number are tolerated.
// Returns an error on empty or unparseable input so CLI callers can
// surface a specific message rather than a silent zero.
func ParseTokenCount(s string) (int, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return 0, fmt.Errorf("empty token count")
	}
	raw = strings.ReplaceAll(raw, ",", "")
	raw = strings.ToLower(raw)

	mult := 1.0
	switch {
	case strings.HasSuffix(raw, "k"):
		mult = 1_000
		raw = strings.TrimSuffix(raw, "k")
	case strings.HasSuffix(raw, "m"):
		mult = 1_000_000
		raw = strings.TrimSuffix(raw, "m")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("no numeric part in token count")
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("cannot parse token count %q: %w", s, err)
	}
	if v < 0 {
		return 0, fmt.Errorf("negative token count %q", s)
	}
	return int(v * mult), nil
}
