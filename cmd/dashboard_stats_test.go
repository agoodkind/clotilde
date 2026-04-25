package cmd

import (
	"testing"
	"time"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
)

func TestProviderStatsFromProto(t *testing.T) {
	now := time.Now().Unix()
	out := providerStatsFromProto([]*clydev1.ProviderStats{{
		Provider:                "openai-codex",
		Requests:                3,
		Inflight:                1,
		Streaming:               1,
		InputTokens:             120,
		OutputTokens:            45,
		CacheReadTokens:         22,
		CacheCreationTokens:     5,
		HitRatio:                0.15,
		EstimatedCostMicrocents: 1234,
		LastSeenUnix:            now,
		Error:                   "boom",
	}})
	if len(out) != 1 {
		t.Fatalf("providers=%d want 1", len(out))
	}
	got := out[0]
	if got.Provider != "openai-codex" || got.Requests != 3 || got.Inflight != 1 || got.Streaming != 1 {
		t.Fatalf("unexpected stats: %+v", got)
	}
	if got.LastSeen.Unix() != now {
		t.Fatalf("last_seen=%d want %d", got.LastSeen.Unix(), now)
	}
}
