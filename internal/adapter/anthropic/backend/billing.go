package anthropicbackend

import (
	"os"
	"strings"
)

// MutateBillingForProbe applies the CLYDE_PROBE_BILLING env var for
// debugging the Anthropic identity check. canonical includes
// cc_version, cc_entrypoint, and cch. Returns "" to omit the
// billing line entirely.
func MutateBillingForProbe(canonical, cliVersion, ccEntrypoint string) string {
	mode := strings.TrimSpace(os.Getenv("CLYDE_PROBE_BILLING"))
	if mode == "" {
		return canonical
	}
	const prefix = "x-anthropic-billing-header: "
	switch mode {
	case "omit":
		return ""
	case "wrong_fp":
		return prefix + "cc_version=" + cliVersion + ".zzz; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "omit_fp":
		return prefix + "cc_version=" + cliVersion + "; cc_entrypoint=" + ccEntrypoint + "; cch=00000;"
	case "bad_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cc_entrypoint=garbage; cch=" + cchVal + ";"
	case "omit_entrypoint":
		fp := extractFingerprint(canonical)
		cchVal := extractBillingCCH(canonical)
		if cchVal == "" {
			cchVal = "00000"
		}
		return prefix + "cc_version=" + cliVersion + "." + fp + "; cch=" + cchVal + ";"
	case "cch_zero":
		return replaceBillingCCH(canonical, "00000")
	case "cch_z":
		return replaceBillingCCH(canonical, "ZZZZZ")
	case "cch_long":
		return replaceBillingCCH(canonical, strings.Repeat("a", 32))
	default:
		// Unknown mode: ship canonical so a typo doesn't silently
		// drop the bucket signal.
		return canonical
	}
}

// replaceBillingCCH swaps the value after `cch=` up to the next `;`.
func replaceBillingCCH(line, newVal string) string {
	const marker = "cch="
	before, after, ok := strings.Cut(line, marker)
	if !ok {
		return line + " cch=" + newVal + ";"
	}
	_, tail, ok2 := strings.Cut(after, ";")
	if !ok2 {
		return before + marker + newVal
	}
	return before + marker + newVal + ";" + tail
}

func extractBillingCCH(line string) string {
	const marker = "cch="
	_, after, ok := strings.Cut(line, marker)
	if !ok {
		return ""
	}
	val, _, _ := strings.Cut(after, ";")
	return val
}

func extractFingerprint(line string) string {
	const verPrefix = "cc_version="
	_, rest, ok := strings.Cut(line, verPrefix)
	if !ok {
		return ""
	}
	verPart, _, ok2 := strings.Cut(rest, ";")
	if !ok2 {
		return ""
	}
	dot := strings.LastIndexByte(verPart, '.')
	if dot < 0 {
		return ""
	}
	return verPart[dot+1:]
}
