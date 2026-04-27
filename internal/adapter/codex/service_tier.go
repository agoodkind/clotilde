package codex

import (
	"encoding/json"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func ServiceTierFromRequest(req adapteropenai.ChatRequest) string {
	if serviceTier := normalizeServiceTier(req.ServiceTier); serviceTier != "" {
		return serviceTier
	}
	return ServiceTierFromMetadata(req.Metadata)
}

func ServiceTierFromMetadata(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return ""
	}
	serviceTier, _ := metadata["service_tier"].(string)
	return normalizeServiceTier(serviceTier)
}

func normalizeServiceTier(serviceTier string) string {
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	switch serviceTier {
	case "fast":
		return "priority"
	default:
		return serviceTier
	}
}
