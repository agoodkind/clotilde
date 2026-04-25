package anthropicbackend

import (
	"strings"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
)

func EffectiveThinkingMode(model adaptermodel.ResolvedModel, strippedModel string) string {
	if strings.EqualFold(strippedModel, "claude-opus-4-7") && model.Thinking == adaptermodel.ThinkingEnabled {
		return adaptermodel.ThinkingAdaptive
	}
	return model.Thinking
}

func StripContextSuffix(model string) string {
	if idx := strings.Index(model, "["); idx >= 0 {
		return strings.TrimSpace(model[:idx])
	}
	return strings.TrimSpace(model)
}

func MaxTokens(req *int) int {
	if req == nil || *req <= 0 {
		return 4096
	}
	if *req > 128000 {
		return 128000
	}
	return *req
}
