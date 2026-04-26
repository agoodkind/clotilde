package codex

import (
    "encoding/json"
    "regexp"
    "strconv"
    "strings"

    adapteropenai "goodkind.io/clyde/internal/adapter/openai"
    "goodkind.io/clyde/internal/adapter/tooltrans"
)

var writeIntentWord = regexp.MustCompile(`(?i)\b(write|save|create|edit|update|modify|patch|apply|commit)\b`)

func HasWriteIntent(req adapteropenai.ChatRequest) bool {
    if len(req.Tools) == 0 {
        return false
    }
    for i := len(req.Messages) - 1; i >= 0; i-- {
        if !strings.EqualFold(req.Messages[i].Role, "user") {
            continue
        }
        text := strings.TrimSpace(adapteropenai.FlattenContent(req.Messages[i].Content))
        return writeIntentWord.MatchString(text)
    }
    return false
}

func LooksLikePathClarification(text string) bool {
    text = strings.ToLower(tooltrans.StripThinkingSentinel(text))
    phrases := []string{
        "where would you like the markdown file saved",
        "need to know the filename",
        "need to know the destination path",
        "clarification on the path",
        "save path",
        "destination path",
        "workspace root",
    }
    for _, phrase := range phrases {
        if strings.Contains(text, phrase) {
            return true
        }
    }
    return false
}

func ChunksContainToolCalls(chunks []tooltrans.OpenAIStreamChunk) bool {
    for _, ch := range chunks {
        for _, choice := range ch.Choices {
            if len(choice.Delta.ToolCalls) > 0 {
                return true
            }
        }
    }
    return false
}

type assembledToolCall struct {
    Name      string
    Arguments strings.Builder
}

func assembleToolCalls(chunks []tooltrans.OpenAIStreamChunk) map[string]*assembledToolCall {
    calls := make(map[string]*assembledToolCall)
    for _, ch := range chunks {
        for _, choice := range ch.Choices {
            for _, tc := range choice.Delta.ToolCalls {
                key := tc.ID
                if key == "" {
                    key = "index:" + strconv.Itoa(tc.Index)
                }
                call := calls[key]
                if call == nil {
                    call = &assembledToolCall{}
                    calls[key] = call
                }
                if tc.Function.Name != "" {
                    call.Name = tc.Function.Name
                }
                if tc.Function.Arguments != "" {
                    call.Arguments.WriteString(tc.Function.Arguments)
                }
            }
        }
    }
    return calls
}

func ToolCallsHaveUsableArguments(chunks []tooltrans.OpenAIStreamChunk) bool {
    for _, call := range assembleToolCalls(chunks) {
        args := strings.TrimSpace(call.Arguments.String())
        if args == "" || args == "{}" || args == "null" {
            continue
        }
        if IsApplyPatchToolName(call.Name) && strings.HasPrefix(args, "*** Begin Patch") {
            return true
        }
        var decoded any
        if err := json.Unmarshal([]byte(args), &decoded); err != nil {
            continue
        }
        switch v := decoded.(type) {
        case map[string]any:
            if len(v) == 0 {
                continue
            }
        case nil:
            continue
        }
        return true
    }
    return false
}

func CollectAssistantText(chunks []tooltrans.OpenAIStreamChunk) string {
    var b strings.Builder
    for _, ch := range chunks {
        for _, choice := range ch.Choices {
            if choice.Delta.Content != "" {
                b.WriteString(choice.Delta.Content)
            }
        }
    }
    return b.String()
}

func ShouldEscalateDirect(req adapteropenai.ChatRequest, chunks []tooltrans.OpenAIStreamChunk, finishReason string) (bool, string) {
    if !HasWriteIntent(req) {
        return false, ""
    }
    if finishReason == "tool_calls" || ChunksContainToolCalls(chunks) {
        if !ToolCallsHaveUsableArguments(chunks) {
            return true, "write_intent_empty_tool_arguments"
        }
        return false, ""
    }
    text := CollectAssistantText(chunks)
    if strings.TrimSpace(text) == "" {
        return true, "write_intent_without_tool_calls"
    }
    lower := strings.ToLower(tooltrans.StripThinkingSentinel(text))
    if strings.Contains(lower, "using the shell") || strings.Contains(lower, "using glob") || strings.Contains(lower, "let’s run `ls`") || strings.Contains(lower, "let's run `ls`") {
        return true, "write_intent_without_tool_calls"
    }
    if LooksLikePathClarification(text) {
        return true, "write_intent_without_tool_calls"
    }
    return true, "write_intent_without_tool_calls"
}
