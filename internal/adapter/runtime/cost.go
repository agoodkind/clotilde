package runtime

import (
	"strings"
)

// ModelRates captures the public per-token billing rates for one
// model, in microcents per token. A microcent is 1e-6 of one US
// cent; using integers avoids floating-point drift when summing
// millions of requests in aggregation pipelines.
//
// Keep rates in sync with Anthropic's published pricing page:
//
//	https://docs.claude.com/en/docs/about-claude/models/overview#model-pricing
//
// Rates below assume the standard Messages API list price. They do
// NOT represent what the user is billed when running against a Max
// subscription bucket; they are the reference rates used to compare
// costs across paths (e.g. Anthropic OAuth vs shunted) so the
// user can pick the cheapest effective route for a given workload.
type ModelRates struct {
	InputPerToken        int64
	OutputPerToken       int64
	CacheWrite5mPerToken int64 // 1.25x input
	CacheWrite1hPerToken int64 // 2x input
	CacheReadPerToken    int64 // 0.1x input
}

// Public-list rates, expressed as microcents-per-token. One million
// tokens at $X.YY translates to (X.YY * 100 * 1_000_000) / 1_000_000
// = X.YY * 100 microcents per token. Example: Sonnet 4 input at $3/M
// tokens = 300 microcents/token.
//
// The map is intentionally tolerant to alias spellings clyde may see
// on the wire (snapshot IDs, context-suffixed variants). Lookup uses
// prefix / substring matching in LookupRates.
var modelRates = map[string]ModelRates{
	"claude-opus-4-6": {
		InputPerToken:        500,  // $5/M
		OutputPerToken:       2500, // $25/M
		CacheWrite5mPerToken: 625,  // $6.25/M (1.25x)
		CacheWrite1hPerToken: 1000, // $10/M   (2x)
		CacheReadPerToken:    50,   // $0.50/M (0.1x)
	},
	"claude-opus-4": {
		InputPerToken:        1500, // $15/M
		OutputPerToken:       7500, // $75/M
		CacheWrite5mPerToken: 1875, // $18.75/M (1.25x)
		CacheWrite1hPerToken: 3000, // $30/M   (2x)
		CacheReadPerToken:    150,  // $1.50/M (0.1x)
	},
	"claude-sonnet-4": {
		InputPerToken:        300,
		OutputPerToken:       1500,
		CacheWrite5mPerToken: 375,
		CacheWrite1hPerToken: 600,
		CacheReadPerToken:    30,
	},
	"claude-haiku-4": {
		InputPerToken:        100,
		OutputPerToken:       500,
		CacheWrite5mPerToken: 125,
		CacheWrite1hPerToken: 200,
		CacheReadPerToken:    10,
	},
}

// LookupRates returns billing rates for the given model id. Matches
// by longest-prefix: "claude-opus-4-7" matches "claude-opus-4".
// Zero-valued rates are returned when nothing matches; callers
// should treat zero-cost results as "unknown model, skip estimate".
func LookupRates(modelID string) (ModelRates, bool) {
	if modelID == "" {
		return ModelRates{}, false
	}
	id := strings.ToLower(modelID)
	var bestKey string
	for k := range modelRates {
		if strings.HasPrefix(id, k) && len(k) > len(bestKey) {
			bestKey = k
		}
	}
	if bestKey == "" {
		return ModelRates{}, false
	}
	return modelRates[bestKey], true
}

// CostInputs contains token counts and TTL settings used by
// EstimateCostMicrocents.
//
// TTL values accepted: "5m" (default), "1h". Any other string is
// treated as 5m since the API defaults to 5m when omitted.
type CostInputs struct {
	ModelID             string
	TTL                 string // "" / "5m" / "1h"
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
}

type CostBreakdown struct {
	InputMicrocents      int64
	OutputMicrocents     int64
	CacheWriteMicrocents int64
	CacheReadMicrocents  int64
	TotalMicrocents      int64
	// HypotheticalNoCacheMicrocents is the cost if every cache-read
	// token had been billed as a fresh input token. Useful for
	// quantifying cache savings.
	HypotheticalNoCacheMicrocents int64
	// CacheSavingsMicrocents = Hypothetical - Total.
	CacheSavingsMicrocents int64
	RatesKnown             bool
}

// EstimateCost returns the zero value when rates are unknown.
func EstimateCost(in CostInputs) CostBreakdown {
	rates, ok := LookupRates(in.ModelID)
	if !ok {
		return CostBreakdown{RatesKnown: false}
	}
	cacheWriteRate := rates.CacheWrite5mPerToken
	if in.TTL == "1h" {
		cacheWriteRate = rates.CacheWrite1hPerToken
	}
	b := CostBreakdown{
		InputMicrocents:      int64(in.InputTokens) * rates.InputPerToken,
		OutputMicrocents:     int64(in.OutputTokens) * rates.OutputPerToken,
		CacheWriteMicrocents: int64(in.CacheCreationTokens) * cacheWriteRate,
		CacheReadMicrocents:  int64(in.CacheReadTokens) * rates.CacheReadPerToken,
		RatesKnown:           true,
	}
	b.TotalMicrocents = b.InputMicrocents + b.OutputMicrocents + b.CacheWriteMicrocents + b.CacheReadMicrocents
	// Had the cache-read tokens been fresh input, they would have cost
	// the full input rate instead of the 0.1x cache-read rate. The
	// write surcharge was paid on the first turn, which we don't
	// subtract here; this measures marginal savings on reads only.
	noCacheInput := int64(in.InputTokens+in.CacheReadTokens) * rates.InputPerToken
	b.HypotheticalNoCacheMicrocents = noCacheInput + b.OutputMicrocents + b.CacheWriteMicrocents
	b.CacheSavingsMicrocents = b.HypotheticalNoCacheMicrocents - b.TotalMicrocents
	return b
}
