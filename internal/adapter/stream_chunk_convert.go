package adapter

import "goodkind.io/clyde/internal/adapter/tooltrans"

func streamChunkFromTooltrans(och tooltrans.OpenAIStreamChunk) StreamChunk {
	out := StreamChunk{
		ID:      och.ID,
		Object:  och.Object,
		Created: och.Created,
		Model:   och.Model,
		Choices: make([]StreamChoice, 0, len(och.Choices)),
	}
	if och.Usage != nil {
		out.Usage = &Usage{
			PromptTokens:     och.Usage.PromptTokens,
			CompletionTokens: och.Usage.CompletionTokens,
			TotalTokens:      och.Usage.TotalTokens,
		}
	}
	for _, c := range och.Choices {
		sc := StreamChoice{
			Index: c.Index,
			Delta: StreamDelta{
				Role:             c.Delta.Role,
				Content:          c.Delta.Content,
				Reasoning:        c.Delta.Reasoning,
				ReasoningContent: c.Delta.ReasoningContent,
				Refusal:          c.Delta.Refusal,
			},
		}
		if c.Logprobs != nil {
			sc.Logprobs = &LogprobsResult{Content: make([]LogprobToken, 0, len(c.Logprobs.Content))}
			for _, t := range c.Logprobs.Content {
				top := make([]TopLogprob, 0, len(t.TopLogprobs))
				for _, x := range t.TopLogprobs {
					top = append(top, TopLogprob{Token: x.Token, Logprob: x.Logprob, Bytes: x.Bytes})
				}
				sc.Logprobs.Content = append(sc.Logprobs.Content, LogprobToken{
					Token: t.Token, Logprob: t.Logprob, Bytes: t.Bytes, TopLogprobs: top,
				})
			}
		}
		sc.FinishReason = c.FinishReason
		for _, tc := range c.Delta.ToolCalls {
			sc.Delta.ToolCalls = append(sc.Delta.ToolCalls, ToolCall{
				Index: tc.Index,
				ID:    tc.ID,
				Type:  tc.Type,
				Function: ToolCallFunction{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		out.Choices = append(out.Choices, sc)
	}
	return out
}
