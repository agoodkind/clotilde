package adapter

import (
	"net/http"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type sseWriter = adapteropenai.SSEWriter

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	return adapteropenai.NewSSEWriter(w)
}

func streamChunkHasVisibleContent(chunk StreamChunk) bool {
	for _, choice := range chunk.Choices {
		if choice.Delta.Content != "" || choice.Delta.Refusal != "" || choice.Delta.Reasoning != "" || choice.Delta.ReasoningContent != "" {
			return true
		}
	}
	return false
}
