package fallback

import (
	"encoding/json"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestBuildRequestMapsMessagesToolsAndSessionID(t *testing.T) {
	req := BuildRequest(RequestBuildInput{
		Model:      "sonnet",
		ModelAlias: "clyde-sonnet-4.5-medium",
		RequestID:  "req-123",
		Messages: []adapteropenai.ChatMessage{
			{Role: "system", Content: json.RawMessage(`"system one"`)},
			{Role: "developer", Content: json.RawMessage(`"developer two"`)},
			{Role: "user", Content: json.RawMessage(`"hello"`)},
			{Role: "assistant", Content: json.RawMessage(`"hi"`)},
			{Role: "tool", Content: json.RawMessage(`"tool output"`)},
		},
		Tools: []adapteropenai.Tool{
			{Function: adapteropenai.ToolFunctionSchema{
				Name:        "Read",
				Description: "read a file",
				Parameters:  json.RawMessage(`{"type":"object"}`),
			}},
		},
		ToolChoice: json.RawMessage(`{"type":"function","function":{"name":"Read"}}`),
	})

	if req.Model != "sonnet" {
		t.Fatalf("Model = %q", req.Model)
	}
	if req.RequestID != "req-123" {
		t.Fatalf("RequestID = %q", req.RequestID)
	}
	if req.System != "system one\n\ndeveloper two" {
		t.Fatalf("System = %q", req.System)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("Messages len = %d, messages = %+v", len(req.Messages), req.Messages)
	}
	if req.Messages[0] != (Message{Role: "user", Content: "hello"}) {
		t.Fatalf("first message = %+v", req.Messages[0])
	}
	if req.Messages[2] != (Message{Role: "user", Content: "tool: tool output"}) {
		t.Fatalf("tool message = %+v", req.Messages[2])
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "Read" || req.Tools[0].Description != "read a file" {
		t.Fatalf("Tools = %+v", req.Tools)
	}
	if string(req.Tools[0].Parameters) != `{"type":"object"}` {
		t.Fatalf("tool parameters = %s", req.Tools[0].Parameters)
	}
	if req.ToolChoice != "Read" {
		t.Fatalf("ToolChoice = %q", req.ToolChoice)
	}
	if req.SessionID == "" {
		t.Fatalf("SessionID is empty")
	}
	if got := BuildRequest(RequestBuildInput{
		ModelAlias: "clyde-sonnet-4.5-medium",
		Messages:   []adapteropenai.ChatMessage{{Role: "user", Content: json.RawMessage(`"hello"`)}},
	}).SessionID; got != req.SessionID {
		t.Fatalf("expected stable SessionID from first user message, got %q want %q", got, req.SessionID)
	}
}

func TestBuildToolsPrefersToolsOverFunctions(t *testing.T) {
	tools := BuildTools(
		[]adapteropenai.Tool{{Function: adapteropenai.ToolFunctionSchema{Name: "ToolName"}}},
		[]adapteropenai.Function{{Name: "FunctionName"}},
	)
	if len(tools) != 1 || tools[0].Name != "ToolName" {
		t.Fatalf("tools = %+v", tools)
	}
}

func TestBuildToolsFallsBackToFunctions(t *testing.T) {
	tools := BuildTools(nil, []adapteropenai.Function{
		{Name: "FunctionName", Description: "legacy shape", Parameters: json.RawMessage(`{"type":"object"}`)},
	})
	if len(tools) != 1 || tools[0].Name != "FunctionName" {
		t.Fatalf("tools = %+v", tools)
	}
	if tools[0].Description != "legacy shape" {
		t.Fatalf("description = %q", tools[0].Description)
	}
}

func TestParseToolChoice(t *testing.T) {
	cases := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{"empty", nil, "auto"},
		{"string", json.RawMessage(`"none"`), "none"},
		{"function object", json.RawMessage(`{"type":"function","function":{"name":"Read"}}`), "Read"},
		{"unknown", json.RawMessage(`{"type":"other"}`), "auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseToolChoice(tc.raw); got != tc.want {
				t.Fatalf("ParseToolChoice = %q want %q", got, tc.want)
			}
		})
	}
}

func TestBuildMessagesFlattensTextPartsAndMergesAdjacentRoles(t *testing.T) {
	system, msgs := BuildMessages([]adapteropenai.ChatMessage{
		{Role: "system", Content: json.RawMessage(`[{"type":"text","text":"sys"}]`)},
		{Role: "user", Content: json.RawMessage(`[{"type":"text","text":"hello "},{"type":"text","text":"world"}]`)},
		{Role: "user", Content: json.RawMessage(`"again"`)},
		{Role: "function", Content: json.RawMessage(`"result"`)},
	})
	if system != "sys" {
		t.Fatalf("system = %q", system)
	}
	if len(msgs) != 1 {
		t.Fatalf("msgs len = %d, msgs = %+v", len(msgs), msgs)
	}
	want := "hello world\n\nagain\n\ntool: result"
	if msgs[0] != (Message{Role: "user", Content: want}) {
		t.Fatalf("message = %+v want role user content %q", msgs[0], want)
	}
}
