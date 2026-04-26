package anthropicbackend

import "goodkind.io/clyde/internal/adapter/tooltrans"

// TranslateRequest is the Anthropic-backend owned request-translation entrypoint.
// The implementation currently delegates to tooltrans while the Phase 8 split is
// in progress, so root call sites no longer depend on tooltrans directly.
func TranslateRequest(req tooltrans.OpenAIRequest, systemPrefix string, maxTokens int) (tooltrans.AnthRequest, error) {
    return tooltrans.TranslateRequest(req, systemPrefix, maxTokens)
}
