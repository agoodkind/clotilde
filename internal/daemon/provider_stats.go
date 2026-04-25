package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/adapter/chatemit"
	"goodkind.io/clyde/internal/config"
)

type providerAggregate struct {
	Provider                   string
	Requests                   int64
	Inflight                   int64
	Streaming                  int64
	InputTokens                int64
	OutputTokens               int64
	CacheReadTokens            int64
	CacheCreationTokens        int64
	DerivedCacheCreationTokens int64
	EstimatedCostMicrocents    int64
	LastSeenUnix               int64
	Error                      string
}

type activeProviderRequest struct {
	Provider  string
	Streaming bool
}

type providerStatsHub struct {
	log          *slog.Logger
	mu           sync.RWMutex
	providers    map[string]*providerAggregate
	active       map[string]activeProviderRequest
	terminalSeen map[string]chatemit.RequestStage
	subscribers  map[chan *clydev1.ProviderStatsEvent]struct{}
	loadedAt     time.Time
}

func newProviderStatsHub(log *slog.Logger) *providerStatsHub {
	h := &providerStatsHub{
		log:          log,
		providers:    make(map[string]*providerAggregate),
		active:       make(map[string]activeProviderRequest),
		terminalSeen: make(map[string]chatemit.RequestStage),
		subscribers:  make(map[chan *clydev1.ProviderStatsEvent]struct{}),
		loadedAt:     time.Now(),
	}
	h.replayLogs()
	return h
}

func (h *providerStatsHub) replayLogs() {
	path, err := resolveProviderStatsLogPath()
	if err != nil {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256*1024), 4*1024*1024)
	for scanner.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		msg, _ := rec["msg"].(string)
		ev, ok := requestEventFromLogRecord(msg, rec)
		if !ok {
			continue
		}
		h.apply(ev, false)
	}
	if err := scanner.Err(); err != nil && h.log != nil {
		h.log.Warn("provider_stats.replay.scan_failed",
			"component", "daemon",
			"err", err,
			"path", path,
		)
	}
	h.loadedAt = time.Now()
}

func resolveProviderStatsLogPath() (string, error) {
	cfg, err := config.LoadGlobalOrDefault()
	if err == nil {
		if p := strings.TrimSpace(cfg.Logging.Paths.Daemon); p != "" {
			return p, nil
		}
	}
	state := config.DefaultStateDir()
	if state == "" {
		return "", errors.New("state dir unavailable")
	}
	primary := filepath.Join(state, "clyde-daemon.jsonl")
	if _, err := os.Stat(primary); err == nil {
		return primary, nil
	}
	return filepath.Join(state, "clyde.jsonl"), nil
}

func requestEventFromLogRecord(msg string, rec map[string]any) (chatemit.RequestEvent, bool) {
	stage := chatemit.RequestStage("")
	switch msg {
	case "adapter.request.started":
		stage = chatemit.RequestStageStarted
	case "adapter.request.stream_opened":
		stage = chatemit.RequestStageStreamOpened
	case "adapter.request.completed":
		stage = chatemit.RequestStageCompleted
	case "adapter.request.failed":
		stage = chatemit.RequestStageFailed
	case "adapter.request.cancelled":
		stage = chatemit.RequestStageCancelled
	default:
		return chatemit.RequestEvent{}, false
	}
	ev := chatemit.RequestEvent{
		Stage:                      stage,
		Provider:                   stringValue(rec, "provider"),
		Backend:                    stringValue(rec, "backend"),
		RequestID:                  stringValue(rec, "request_id"),
		Alias:                      stringValue(rec, "alias"),
		ModelID:                    stringValue(rec, "model"),
		Stream:                     boolValue(rec, "stream"),
		FinishReason:               stringValue(rec, "finish_reason"),
		TokensIn:                   intValue(rec, "prompt_tokens"),
		TokensOut:                  intValue(rec, "completion_tokens"),
		CacheReadTokens:            intValue(rec, "cache_read_tokens"),
		CacheCreationTokens:        intValue(rec, "cache_creation_tokens"),
		DerivedCacheCreationTokens: intValue(rec, "derived_cache_creation_tokens"),
		CostMicrocents:             int64Value(rec, "cost_microcents"),
		DurationMs:                 int64Value(rec, "duration_ms"),
		Err:                        stringValue(rec, "error"),
	}
	if ev.Err == "" {
		ev.Err = stringValue(rec, "err")
	}
	if ev.Provider == "" || ev.RequestID == "" {
		return chatemit.RequestEvent{}, false
	}
	return ev, true
}

func stringValue(rec map[string]any, key string) string {
	v, _ := rec[key]
	switch t := v.(type) {
	case string:
		return t
	default:
		return ""
	}
}

func boolValue(rec map[string]any, key string) bool {
	v, _ := rec[key]
	b, _ := v.(bool)
	return b
}

func intValue(rec map[string]any, key string) int {
	return int(int64Value(rec, key))
}

func int64Value(rec map[string]any, key string) int64 {
	v, _ := rec[key]
	switch t := v.(type) {
	case float64:
		return int64(t)
	case int:
		return int64(t)
	case int64:
		return t
	case json.Number:
		n, _ := t.Int64()
		return n
	default:
		return 0
	}
}

func providerRequestKey(provider, requestID string) string {
	return provider + "\x00" + requestID
}

func (h *providerStatsHub) ensureProvider(provider string) *providerAggregate {
	agg, ok := h.providers[provider]
	if ok {
		return agg
	}
	agg = &providerAggregate{Provider: provider}
	h.providers[provider] = agg
	return agg
}

func (h *providerStatsHub) Record(ctx context.Context, ev chatemit.RequestEvent) {
	h.apply(ev, true)
	if h.log != nil {
		h.log.DebugContext(ctx, "provider_stats.recorded",
			"component", "daemon",
			"provider", ev.Provider,
			"request_id", ev.RequestID,
			"stage", string(ev.Stage),
		)
	}
}

func (h *providerStatsHub) apply(ev chatemit.RequestEvent, broadcast bool) {
	if strings.TrimSpace(ev.Provider) == "" || strings.TrimSpace(ev.RequestID) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	agg := h.ensureProvider(ev.Provider)
	if agg.LastSeenUnix == 0 {
		agg.LastSeenUnix = time.Now().Unix()
	}
	key := providerRequestKey(ev.Provider, ev.RequestID)

	switch ev.Stage {
	case chatemit.RequestStageStarted:
		delete(h.terminalSeen, key)
		if _, exists := h.active[key]; !exists {
			h.active[key] = activeProviderRequest{Provider: ev.Provider, Streaming: false}
			agg.Inflight++
		}
	case chatemit.RequestStageStreamOpened:
		active, exists := h.active[key]
		if !exists {
			active = activeProviderRequest{Provider: ev.Provider}
		}
		if !active.Streaming {
			active.Streaming = true
			h.active[key] = active
			agg.Streaming++
			if agg.Inflight == 0 {
				agg.Inflight++
			}
		}
	case chatemit.RequestStageCompleted, chatemit.RequestStageFailed, chatemit.RequestStageCancelled:
		if _, seen := h.terminalSeen[key]; seen {
			return
		}
		h.terminalSeen[key] = ev.Stage
		if active, exists := h.active[key]; exists {
			if agg.Inflight > 0 {
				agg.Inflight--
			}
			if active.Streaming && agg.Streaming > 0 {
				agg.Streaming--
			}
			delete(h.active, key)
		}
		agg.Requests++
		agg.InputTokens += int64(ev.TokensIn)
		agg.OutputTokens += int64(ev.TokensOut)
		agg.CacheReadTokens += int64(ev.CacheReadTokens)
		agg.CacheCreationTokens += int64(ev.CacheCreationTokens)
		agg.DerivedCacheCreationTokens += int64(ev.DerivedCacheCreationTokens)
		agg.EstimatedCostMicrocents += ev.CostMicrocents
		if ev.Err != "" {
			agg.Error = ev.Err
		}
	}
	agg.LastSeenUnix = time.Now().Unix()
	stats := h.protoFor(agg)
	if broadcast {
		h.broadcastLocked(stats)
	}
}

func (h *providerStatsHub) protoFor(agg *providerAggregate) *clydev1.ProviderStats {
	hitRatio := 0.0
	if denom := agg.InputTokens + agg.CacheReadTokens; denom > 0 {
		hitRatio = float64(agg.CacheReadTokens) / float64(denom)
	}
	return &clydev1.ProviderStats{
		Provider:                   agg.Provider,
		Requests:                   int32(agg.Requests),
		Inflight:                   int32(agg.Inflight),
		Streaming:                  int32(agg.Streaming),
		InputTokens:                agg.InputTokens,
		OutputTokens:               agg.OutputTokens,
		CacheReadTokens:            agg.CacheReadTokens,
		CacheCreationTokens:        agg.CacheCreationTokens,
		DerivedCacheCreationTokens: agg.DerivedCacheCreationTokens,
		HitRatio:                   hitRatio,
		EstimatedCostMicrocents:    agg.EstimatedCostMicrocents,
		LastSeenUnix:               agg.LastSeenUnix,
		Error:                      agg.Error,
	}
}

func (h *providerStatsHub) snapshot() []*clydev1.ProviderStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]*clydev1.ProviderStats, 0, len(h.providers))
	for _, agg := range h.providers {
		out = append(out, h.protoFor(agg))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Inflight != out[j].Inflight {
			return out[i].Inflight > out[j].Inflight
		}
		if out[i].LastSeenUnix != out[j].LastSeenUnix {
			return out[i].LastSeenUnix > out[j].LastSeenUnix
		}
		return out[i].Provider < out[j].Provider
	})
	return out
}

func (h *providerStatsHub) loadedAtUnix() int64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.loadedAt.Unix()
}

func (h *providerStatsHub) subscribe() chan *clydev1.ProviderStatsEvent {
	ch := make(chan *clydev1.ProviderStatsEvent, 32)
	h.mu.Lock()
	h.subscribers[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *providerStatsHub) unsubscribe(ch chan *clydev1.ProviderStatsEvent) {
	h.mu.Lock()
	delete(h.subscribers, ch)
	h.mu.Unlock()
	close(ch)
}

func (h *providerStatsHub) broadcastLocked(stats *clydev1.ProviderStats) {
	ev := &clydev1.ProviderStatsEvent{
		Stats:         stats,
		EmittedAtUnix: time.Now().Unix(),
	}
	for ch := range h.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}
