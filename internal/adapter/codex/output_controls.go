package codex

import (
	"encoding/json"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type OutputControls struct {
	MaxCompletion        *int            `json:"max_completion_tokens,omitempty"`
	Text                 json.RawMessage `json:"text,omitempty"`
	Truncation           string          `json:"truncation,omitempty"`
	PromptCacheRetention string          `json:"prompt_cache_retention,omitempty"`
}

func BuildOutputControls(req adapteropenai.ChatRequest) OutputControls {
	return OutputControls{
		MaxCompletion:        firstInt(req.MaxComplTokens, req.MaxOutputTokens, req.MaxTokens),
		Text:                 validJSONObject(req.Text),
		Truncation:           normalizedTruncation(req.Truncation),
		PromptCacheRetention: normalizedPromptCacheRetention(req.PromptCacheRetention),
	}
}

func firstInt(values ...*int) *int {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func validJSONObject(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if !json.Valid(raw) {
		return nil
	}
	if trimmed[0] != '{' {
		return nil
	}
	return raw
}

func normalizedTruncation(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "auto", "disabled":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func normalizedPromptCacheRetention(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "24h":
		return "24h"
	default:
		return ""
	}
}
