package anthropicbackend

import (
	"strings"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
)

// EffectiveThinkingMode returns the wire-level thinking mode for the
// resolved model. The registry resolves per-family fallbacks
// (including the historical claude-opus-4-7 enabled-to-adaptive remap)
// at construction time, so this function is a passthrough today. The
// strippedModel parameter is kept for callers that still pass it; the
// mapping does not depend on the wire model id at request time.
func EffectiveThinkingMode(model adaptermodel.ResolvedModel, strippedModel string) string {
	_ = strippedModel
	return model.Thinking
}

func StripContextSuffix(model string) string {
	if prefix, _, ok := strings.Cut(model, "["); ok {
		return strings.TrimSpace(prefix)
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

func ResolveMaxTokens(req *int, model adaptermodel.ResolvedModel) int {
	maxTokens := MaxTokens(req)
	if (req == nil || *req <= 0) && model.MaxOutputTokens > 0 {
		maxTokens = model.MaxOutputTokens
	}
	if model.MaxOutputTokens > 0 && maxTokens > model.MaxOutputTokens {
		maxTokens = model.MaxOutputTokens
	}
	return maxTokens
}

func ApplyThinkingConfig(req *anthropic.Request, model adaptermodel.ResolvedModel, strippedModel string) {
	switch EffectiveThinkingMode(model, strippedModel) {
	case adaptermodel.ThinkingAdaptive:
		req.Thinking = &anthropic.Thinking{
			Type:    "adaptive",
			Display: "summarized",
		}
	case adaptermodel.ThinkingEnabled:
		cap := model.MaxOutputTokens
		if cap <= 0 {
			cap = req.MaxTokens
		}
		if cap < 1025 {
			cap = 1025
		}
		req.MaxTokens = cap
		req.Thinking = &anthropic.Thinking{
			Type:         "enabled",
			BudgetTokens: cap - 1,
			Display:      "summarized",
		}
	case adaptermodel.ThinkingDisabled:
		req.Thinking = &anthropic.Thinking{Type: "disabled"}
	}
}
