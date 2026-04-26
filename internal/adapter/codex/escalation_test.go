package codex

import (
    "testing"

    adapteropenai "goodkind.io/clyde/internal/adapter/openai"
    "goodkind.io/clyde/internal/adapter/tooltrans"
)

func rawMessage(text string) []byte { return []byte(text) }

func TestShouldEscalateDirectForWriteIntentClarification(t *testing.T) {
    req := adapteropenai.ChatRequest{
        Tools: []adapteropenai.Tool{{
            Type: "function",
            Function: adapteropenai.ToolFunctionSchema{
                Name:       "WriteFile",
                Parameters: rawMessage(`{"type":"object"}`),
            },
        }},
        Messages: []adapteropenai.ChatMessage{{Role: "user", Content: rawMessage(`"Please write your answer to a markdown file on disk"`)}},
    }
    chunks := []tooltrans.OpenAIStreamChunk{{
        Choices: []tooltrans.OpenAIStreamChoice{{
            Delta: tooltrans.OpenAIStreamDelta{
                Content: "I need to know the destination path. Where would you like the Markdown file saved?",
            },
        }},
    }}
    escalate, reason := ShouldEscalateDirect(req, chunks, "stop")
    if !escalate {
        t.Fatalf("expected escalation")
    }
    if reason == "" {
        t.Fatalf("expected reason")
    }
}

func TestShouldNotEscalateWhenToolCallsPresent(t *testing.T) {
    req := adapteropenai.ChatRequest{
        Tools: []adapteropenai.Tool{{
            Type: "function",
            Function: adapteropenai.ToolFunctionSchema{
                Name:       "WriteFile",
                Parameters: rawMessage(`{"type":"object"}`),
            },
        }},
        Messages: []adapteropenai.ChatMessage{{Role: "user", Content: rawMessage(`"Please write your answer to a markdown file on disk"`)}},
    }
    chunks := []tooltrans.OpenAIStreamChunk{{
        Choices: []tooltrans.OpenAIStreamChoice{{
            Delta: tooltrans.OpenAIStreamDelta{
                ToolCalls: []tooltrans.OpenAIToolCall{{
                    Index: 0,
                    ID:    "call_1",
                    Type:  "function",
                    Function: tooltrans.OpenAIToolCallFunction{
                        Name:      "WriteFile",
                        Arguments: `{"path":"out.md"}`,
                    },
                }},
            },
        }},
    }}
    escalate, _ := ShouldEscalateDirect(req, chunks, "tool_calls")
    if escalate {
        t.Fatalf("did not expect escalation")
    }
}

func TestShouldEscalateWhenToolCallsHaveEmptyArguments(t *testing.T) {
    req := adapteropenai.ChatRequest{
        Tools: []adapteropenai.Tool{{
            Type: "function",
            Function: adapteropenai.ToolFunctionSchema{
                Name:       "WriteFile",
                Parameters: rawMessage(`{"type":"object"}`),
            },
        }},
        Messages: []adapteropenai.ChatMessage{{Role: "user", Content: rawMessage(`"Please write your answer to a markdown file on disk"`)}},
    }
    chunks := []tooltrans.OpenAIStreamChunk{{
        Choices: []tooltrans.OpenAIStreamChoice{{
            Delta: tooltrans.OpenAIStreamDelta{
                ToolCalls: []tooltrans.OpenAIToolCall{{
                    Index: 0,
                    ID:    "call_1",
                    Type:  "function",
                    Function: tooltrans.OpenAIToolCallFunction{
                        Name:      "ReadFile",
                        Arguments: "{}",
                    },
                }},
            },
        }},
    }}
    escalate, reason := ShouldEscalateDirect(req, chunks, "tool_calls")
    if !escalate {
        t.Fatalf("expected escalation")
    }
    if reason != "write_intent_empty_tool_arguments" {
        t.Fatalf("reason=%q", reason)
    }
}

func TestShouldEscalateWhenToolCallsAssembleToEmptyObject(t *testing.T) {
    req := adapteropenai.ChatRequest{
        Tools: []adapteropenai.Tool{{
            Type: "function",
            Function: adapteropenai.ToolFunctionSchema{
                Name:       "Glob",
                Parameters: rawMessage(`{"type":"object","required":["glob_pattern"]}`),
            },
        }},
        Messages: []adapteropenai.ChatMessage{{Role: "user", Content: rawMessage(`"Please write your answer to a markdown file on disk"`)}},
    }
    chunks := []tooltrans.OpenAIStreamChunk{
        {Choices: []tooltrans.OpenAIStreamChoice{{Delta: tooltrans.OpenAIStreamDelta{ToolCalls: []tooltrans.OpenAIToolCall{{Index: 0, ID: "call_1", Type: "function", Function: tooltrans.OpenAIToolCallFunction{Name: "Glob"}}}}}}},
        {Choices: []tooltrans.OpenAIStreamChoice{{Delta: tooltrans.OpenAIStreamDelta{ToolCalls: []tooltrans.OpenAIToolCall{{Index: 0, ID: "call_1", Type: "function", Function: tooltrans.OpenAIToolCallFunction{Arguments: "{"}}}}}}},
        {Choices: []tooltrans.OpenAIStreamChoice{{Delta: tooltrans.OpenAIStreamDelta{ToolCalls: []tooltrans.OpenAIToolCall{{Index: 0, ID: "call_1", Type: "function", Function: tooltrans.OpenAIToolCallFunction{Arguments: "}"}}}}}}},
    }
    escalate, reason := ShouldEscalateDirect(req, chunks, "tool_calls")
    if !escalate {
        t.Fatalf("expected escalation")
    }
    if reason != "write_intent_empty_tool_arguments" {
        t.Fatalf("reason=%q", reason)
    }
}
