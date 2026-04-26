package codex

import adapteropenai "goodkind.io/clyde/internal/adapter/openai"

type OutputControls struct {
    MaxCompletion *int `json:"max_completion_tokens,omitempty"`
}

func BuildOutputControls(req adapteropenai.ChatRequest) OutputControls {
    return OutputControls{
        MaxCompletion: req.MaxComplTokens,
    }
}
