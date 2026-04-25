package runtime

import "testing"

func TestLookupRatesMatchesLongestPrefix(t *testing.T) {
	r, ok := LookupRates("claude-sonnet-4-6")
	if !ok {
		t.Fatalf("expected rates for claude-sonnet-4-6")
	}
	if r.InputPerToken != 300 {
		t.Fatalf("unexpected input rate: %d", r.InputPerToken)
	}
	if _, ok := LookupRates("unknown-model"); ok {
		t.Fatalf("lookup for unknown model should fail")
	}
}

func TestLookupRatesPrefersExactOpus46Prefix(t *testing.T) {
	r, ok := LookupRates("claude-opus-4-6")
	if !ok {
		t.Fatalf("expected rates for claude-opus-4-6")
	}
	if r.InputPerToken != 500 {
		t.Fatalf("unexpected opus 4.6 input rate: %d", r.InputPerToken)
	}
	if r.OutputPerToken != 2500 {
		t.Fatalf("unexpected opus 4.6 output rate: %d", r.OutputPerToken)
	}
}

func TestEstimateCostAppliesCacheWriteTTL(t *testing.T) {
	in := CostInputs{
		ModelID:             "claude-sonnet-4",
		TTL:                 "5m",
		InputTokens:         1000,
		OutputTokens:        500,
		CacheCreationTokens: 200,
		CacheReadTokens:     800,
	}
	b := EstimateCost(in)
	if !b.RatesKnown {
		t.Fatalf("rates should be known for sonnet-4")
	}
	// Input: 1000 * 300 = 300_000 microcents
	// Output: 500 * 1500 = 750_000
	// Cache write (5m): 200 * 375 = 75_000
	// Cache read: 800 * 30 = 24_000
	// Total: 1_149_000
	if b.TotalMicrocents != 1_149_000 {
		t.Fatalf("unexpected total microcents: %d", b.TotalMicrocents)
	}

	// 1h TTL doubles the cache-write rate to 600 mc/tok on sonnet-4.
	in.TTL = "1h"
	b1h := EstimateCost(in)
	if b1h.CacheWriteMicrocents != 200*600 {
		t.Fatalf("1h cache write should be 200*600, got %d", b1h.CacheWriteMicrocents)
	}
}

func TestEstimateCostSavingsVsNoCache(t *testing.T) {
	in := CostInputs{
		ModelID:         "claude-sonnet-4",
		InputTokens:     100,
		OutputTokens:    10,
		CacheReadTokens: 10_000,
	}
	b := EstimateCost(in)
	// No-cache hypothetical adds 10_000 input tokens at full rate.
	// Actual billed read at 0.1x.
	// Savings = (10_000 * 300) - (10_000 * 30) = 2_700_000 microcents.
	if b.CacheSavingsMicrocents != 2_700_000 {
		t.Fatalf("unexpected savings: %d", b.CacheSavingsMicrocents)
	}
}
