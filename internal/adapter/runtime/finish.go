package runtime

// EmitUsageChunk emits one SSE chunk containing usage telemetry.
func EmitUsageChunk(emit func(StreamChunk) error, reqID string, modelAlias string, created int64, usage Usage) error {
	return emit(StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelAlias,
		Choices: []StreamChoice{},
		Usage:   &usage,
	})
}

// EmitFinishChunk emits one SSE chunk containing a finish_reason delta.
func EmitFinishChunk(emit func(StreamChunk) error, reqID string, modelAlias string, created int64, finishReason string) error {
	return emit(StreamChunk{
		ID:      reqID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   modelAlias,
		Choices: []StreamChoice{{
			Index:        0,
			Delta:        StreamDelta{},
			FinishReason: &finishReason,
		}},
	})
}
