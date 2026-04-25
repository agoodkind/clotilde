package cmd

import (
	"context"
	"log/slog"
	"time"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/ui"
)

func loadDashboardStats(ctx context.Context) (ui.DashboardStats, error) {
	resp, err := daemon.GetProviderStats(ctx)
	if err != nil {
		return ui.DashboardStats{}, err
	}
	stats := ui.DashboardStats{
		Providers: providerStatsFromProto(resp.GetProviders()),
		LoadedAt:  time.Unix(resp.GetLoadedAtUnix(), 0),
	}
	return stats, nil
}

func providerStatsFromProto(list []*clydev1.ProviderStats) []ui.ProviderStats {
	out := make([]ui.ProviderStats, 0, len(list))
	for _, item := range list {
		if item == nil {
			continue
		}
		out = append(out, ui.ProviderStats{
			Provider:                item.GetProvider(),
			Requests:                int(item.GetRequests()),
			Inflight:                int(item.GetInflight()),
			Streaming:               int(item.GetStreaming()),
			InputTokens:             item.GetInputTokens(),
			OutputTokens:            item.GetOutputTokens(),
			CacheReadTokens:         item.GetCacheReadTokens(),
			CacheCreationTokens:     item.GetCacheCreationTokens(),
			DerivedCacheCreationTokens: item.GetDerivedCacheCreationTokens(),
			HitRatio:                item.GetHitRatio(),
			EstimatedCostMicrocents: item.GetEstimatedCostMicrocents(),
			LastSeen:                time.Unix(item.GetLastSeenUnix(), 0),
			Error:                   item.GetError(),
		})
	}
	slog.Debug("dashboard.stats.loaded",
		"component", "tui",
		"providers", len(out),
	)
	return out
}
