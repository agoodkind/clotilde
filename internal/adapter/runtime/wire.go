package runtime

import adapteropenai "goodkind.io/clyde/internal/adapter/openai"

type (
	ChatResponse        = adapteropenai.ChatResponse
	ChatChoice          = adapteropenai.ChatChoice
	ChatMessage         = adapteropenai.ChatMessage
	MessageAnnotation   = adapteropenai.MessageAnnotation
	URLCitation         = adapteropenai.URLCitation
	ToolCall            = adapteropenai.ToolCall
	ToolCallFunction    = adapteropenai.ToolCallFunction
	LogprobsResult      = adapteropenai.LogprobsResult
	LogprobToken        = adapteropenai.LogprobToken
	TopLogprob          = adapteropenai.TopLogprob
	Usage               = adapteropenai.Usage
	PromptTokensDetails = adapteropenai.PromptTokensDetails
	StreamChunk         = adapteropenai.StreamChunk
	StreamChoice        = adapteropenai.StreamChoice
	StreamDelta         = adapteropenai.StreamDelta
)
