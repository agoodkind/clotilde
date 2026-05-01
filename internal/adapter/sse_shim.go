package adapter

func streamChunkHasVisibleContent(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" || choice.Delta.Refusal != "" || choice.Delta.Reasoning != "" || choice.Delta.ReasoningContent != "" {
			return true
		}
	}
	return false
}
