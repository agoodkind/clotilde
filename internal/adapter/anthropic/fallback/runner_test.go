package fallback

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

const fakeClaudeToolScript = `#!/usr/bin/env sh
echo '{"type":"system","subtype":"init"}'
echo '{"type":"assistant","message":{"content":[{"type":"text","text":"{\"tool_calls\":[{\"name\":\"Read\",\"arguments\":{\"path\":\"README.md\"}}]}"}]}}'
echo '{"type":"result","usage":{"input_tokens":11,"output_tokens":2},"stop_reason":"tool_use"}'
`

func TestCollectOpenAIWrapsCollectAndResponseMapping(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeScript)
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: t.TempDir()})

	run, err := c.CollectOpenAI(context.Background(), Request{
		Model:    "haiku",
		Messages: []Message{{Role: "user", Content: "hi"}},
	}, CollectOpenAIInput{
		RequestID:         "req-collect",
		ModelAlias:        "alias",
		SystemFingerprint: "fp",
	})
	if err != nil {
		t.Fatalf("CollectOpenAI: %v", err)
	}
	if run.Raw.Text != "hello world" {
		t.Fatalf("raw text = %q", run.Raw.Text)
	}
	if run.Final.Response.ID != "req-collect" || run.Final.Response.SystemFingerprint != "fp" {
		t.Fatalf("response = %+v", run.Final.Response)
	}
	var text string
	if err := json.Unmarshal(run.Final.Response.Choices[0].Message.Content, &text); err != nil {
		t.Fatalf("unmarshal content: %v", err)
	}
	if text != "hello world" {
		t.Fatalf("content = %q", text)
	}
}

func TestStreamOpenAIEmitsLiveChunksForPlainText(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeScript)
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: t.TempDir()})
	var chunks []adapteropenai.StreamChunk
	run, err := c.StreamOpenAI(context.Background(), Request{
		Model:      "haiku",
		Messages:   []Message{{Role: "user", Content: "hi"}},
		ToolChoice: "none",
	}, StreamOpenAIInput{
		RequestID:  "req-stream",
		ModelAlias: "alias",
		Created:    123,
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamOpenAI: %v", err)
	}
	if run.Buffered {
		t.Fatalf("expected unbuffered run")
	}
	if len(chunks) != 2 {
		t.Fatalf("chunks len = %d, chunks = %+v", len(chunks), chunks)
	}
	if chunks[0].Choices[0].Delta.Role != "assistant" {
		t.Fatalf("first chunk = %+v", chunks[0])
	}
	if chunks[0].Choices[0].Delta.Content != "hello " || chunks[1].Choices[0].Delta.Content != "world" {
		t.Fatalf("chunks = %+v", chunks)
	}
	if run.Plan.FinishReason != "stop" {
		t.Fatalf("finish = %q", run.Plan.FinishReason)
	}
}

func TestStreamOpenAIBuffersToolRuns(t *testing.T) {
	bin := writeFakeBinary(t, fakeClaudeToolScript)
	c := New(Config{Binary: bin, Timeout: 5 * time.Second, ScratchDir: t.TempDir()})
	var chunks []adapteropenai.StreamChunk
	run, err := c.StreamOpenAI(context.Background(), Request{
		Model: "haiku",
		Messages: []Message{
			{Role: "user", Content: "read"},
		},
		Tools:      []Tool{{Name: "Read"}},
		ToolChoice: "auto",
	}, StreamOpenAIInput{
		RequestID:  "req-tool",
		ModelAlias: "alias",
		Created:    123,
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamOpenAI: %v", err)
	}
	if !run.Buffered {
		t.Fatalf("expected buffered run")
	}
	if len(chunks) == 0 {
		t.Fatalf("expected replay chunks")
	}
	if run.Plan.FinishReason != "tool_calls" {
		t.Fatalf("finish = %q", run.Plan.FinishReason)
	}
	foundTool := false
	for _, ch := range chunks {
		if len(ch.Choices) > 0 && len(ch.Choices[0].Delta.ToolCalls) > 0 {
			foundTool = true
		}
	}
	if !foundTool {
		t.Fatalf("expected tool call chunk, chunks = %+v", chunks)
	}
}

func TestPrepareTranscriptResumeWritesTranscriptAndMutatesRequest(t *testing.T) {
	workspace := t.TempDir()
	claudeHome := filepath.Join(t.TempDir(), ".claude")
	req := Request{
		SessionID: "11111111-1111-4111-8111-111111111111",
		Messages: []Message{
			{Role: "user", Content: "first"},
			{Role: "assistant", Content: "answer"},
			{Role: "user", Content: "latest"},
		},
	}
	res := PrepareTranscriptResume(&req, TranscriptResumeConfig{
		WorkspaceDir: workspace,
		ClaudeHome:   claudeHome,
		Now: func() time.Time {
			return time.Unix(10, 0).UTC()
		},
	})
	if res.Err != nil {
		t.Fatalf("PrepareTranscriptResume: %v", res.Err)
	}
	if !res.Resumed || !req.Resume || req.WorkspaceDir != workspace {
		t.Fatalf("resume result = %+v req = %+v", res, req)
	}
	if res.PriorTurns != 2 {
		t.Fatalf("PriorTurns = %d", res.PriorTurns)
	}
	if res.Path == "" {
		t.Fatalf("expected transcript path")
	}
}
