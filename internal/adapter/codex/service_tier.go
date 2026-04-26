package codex

import (
    "encoding/json"
    "strings"
)

func ServiceTierFromMetadata(raw json.RawMessage) string {
    if len(raw) == 0 {
        return ""
    }
    var metadata map[string]any
    if err := json.Unmarshal(raw, &metadata); err != nil {
        return ""
    }
    serviceTier, _ := metadata["service_tier"].(string)
    serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
    switch serviceTier {
    case "fast":
        return "priority"
    default:
        return serviceTier
    }
}
