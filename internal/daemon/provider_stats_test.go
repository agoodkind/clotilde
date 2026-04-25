package daemon

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

func testProviderStatsLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestProviderStatsHubTracksInflightStreamingAndTerminal(t *testing.T) {
	hub := newProviderStatsHub(testProviderStatsLogger())
	hub.providers = map[string]*providerAggregate{}
	hub.active = map[string]activeProviderRequest{}
	hub.terminalSeen = map[string]adapterruntime.RequestStage{}

	ctx := context.Background()
	hub.Record(ctx, adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageStarted, Provider: "openai-codex", RequestID: "req-1"})
	stats := hub.snapshot()
	if len(stats) != 1 || stats[0].GetInflight() != 1 || stats[0].GetStreaming() != 0 {
		t.Fatalf("after start: %+v", stats)
	}

	hub.Record(ctx, adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageStreamOpened, Provider: "openai-codex", RequestID: "req-1"})
	stats = hub.snapshot()
	if stats[0].GetInflight() != 1 || stats[0].GetStreaming() != 1 {
		t.Fatalf("after stream open: %+v", stats[0])
	}

	hub.Record(ctx, adapterruntime.RequestEvent{
		Stage:                      adapterruntime.RequestStageCompleted,
		Provider:                   "openai-codex",
		RequestID:                  "req-1",
		TokensIn:                   100,
		TokensOut:                  50,
		CacheReadTokens:            25,
		CacheCreationTokens:        5,
		DerivedCacheCreationTokens: 9,
		CostMicrocents:             700,
	})
	stats = hub.snapshot()
	got := stats[0]
	if got.GetInflight() != 0 || got.GetStreaming() != 0 || got.GetRequests() != 1 {
		t.Fatalf("after terminal counts wrong: %+v", got)
	}
	if got.GetInputTokens() != 100 || got.GetOutputTokens() != 50 || got.GetCacheReadTokens() != 25 || got.GetCacheCreationTokens() != 5 || got.GetDerivedCacheCreationTokens() != 9 {
		t.Fatalf("after terminal tokens wrong: %+v", got)
	}
}

func TestProviderStatsHubIgnoresDuplicateTerminal(t *testing.T) {
	hub := newProviderStatsHub(testProviderStatsLogger())
	hub.providers = map[string]*providerAggregate{}
	hub.active = map[string]activeProviderRequest{}
	hub.terminalSeen = map[string]adapterruntime.RequestStage{}

	ctx := context.Background()
	hub.Record(ctx, adapterruntime.RequestEvent{Stage: adapterruntime.RequestStageStarted, Provider: "anthropic-oauth", RequestID: "req-2"})
	ev := adapterruntime.RequestEvent{
		Stage:     adapterruntime.RequestStageFailed,
		Provider:  "anthropic-oauth",
		RequestID: "req-2",
		Err:       "boom",
	}
	hub.Record(ctx, ev)
	hub.Record(ctx, ev)

	stats := hub.snapshot()
	if len(stats) != 1 || stats[0].GetRequests() != 1 {
		t.Fatalf("duplicate terminal counted twice: %+v", stats)
	}
	if stats[0].GetInflight() != 0 {
		t.Fatalf("inflight not cleared: %+v", stats[0])
	}
}

func TestProviderStatsHubBroadcastsToSubscribers(t *testing.T) {
	hub := newProviderStatsHub(testProviderStatsLogger())
	hub.providers = map[string]*providerAggregate{}
	hub.active = map[string]activeProviderRequest{}
	hub.terminalSeen = map[string]adapterruntime.RequestStage{}

	ch := hub.subscribe()
	defer hub.unsubscribe(ch)

	hub.Record(context.Background(), adapterruntime.RequestEvent{
		Stage:     adapterruntime.RequestStageStarted,
		Provider:  "openai-codex",
		RequestID: "req-3",
	})

	select {
	case ev := <-ch:
		if ev.GetStats().GetProvider() != "openai-codex" || ev.GetStats().GetInflight() != 1 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for provider stats event")
	}
}
