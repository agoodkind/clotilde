// OpenAI wire mirrors now live in internal/adapter/openai.
package tooltrans

import adapteropenai "goodkind.io/clyde/internal/adapter/openai"

type OpenAIRequest = adapteropenai.ChatRequest
type StreamOptions = adapteropenai.StreamOptions
type OpenAITool = adapteropenai.Tool
type OpenAIToolFunctionSchema = adapteropenai.ToolFunctionSchema
type OpenAIFunction = adapteropenai.Function
type OpenAIMessage = adapteropenai.ChatMessage
type OpenAIMessageAnnotation = adapteropenai.MessageAnnotation
type OpenAIURLCitation = adapteropenai.URLCitation
type OpenAIToolCall = adapteropenai.ToolCall
type OpenAIToolCallFunction = adapteropenai.ToolCallFunction
type OpenAIContentPart = adapteropenai.ContentPart
type OpenAIImageURLPart = adapteropenai.ImageURLPart
type OpenAIAudioInputRef = adapteropenai.AudioInputRef
type OpenAIChatResponse = adapteropenai.ChatResponse
type OpenAIChatChoice = adapteropenai.ChatChoice
type OpenAILogprobsResult = adapteropenai.LogprobsResult
type OpenAILogprobToken = adapteropenai.LogprobToken
type OpenAITopLogprob = adapteropenai.TopLogprob
type OpenAIStreamChunk = adapteropenai.StreamChunk
type OpenAIStreamChoice = adapteropenai.StreamChoice
type OpenAIStreamDelta = adapteropenai.StreamDelta
type OpenAIUsage = adapteropenai.Usage
type OpenAIPromptTokensDetails = adapteropenai.PromptTokensDetails
