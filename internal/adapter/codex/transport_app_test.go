package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type fakeRPCClient struct {
	sent    []rpcCall
	next    []RPCMessage
	nextErr error
	closed  bool
}

type rpcCall struct {
	id     int
	method string
	params rpcRequestParams
}

func (f *fakeRPCClient) SendInitialize(id int, params RPCInitializeParams) error {
	f.sent = append(f.sent, rpcCall{id: id, method: params.rpcMethod(), params: params})
	return nil
}

func (f *fakeRPCClient) NotifyInitialized() error {
	f.sent = append(f.sent, rpcCall{id: 0, method: "initialized"})
	return nil
}

func (f *fakeRPCClient) SendThreadStart(id int, params RPCThreadStartParams) error {
	f.sent = append(f.sent, rpcCall{id: id, method: params.rpcMethod(), params: params})
	return nil
}

func (f *fakeRPCClient) SendTurnStart(id int, params RPCTurnStartParams) error {
	f.sent = append(f.sent, rpcCall{id: id, method: params.rpcMethod(), params: params})
	return nil
}

func (f *fakeRPCClient) SendThreadArchive(id int, params RPCThreadArchiveParams) error {
	f.sent = append(f.sent, rpcCall{id: id, method: params.rpcMethod(), params: params})
	return nil
}

func (f *fakeRPCClient) Next() (RPCMessage, error) {
	if len(f.next) > 0 {
		msg := f.next[0]
		f.next = f.next[1:]
		return msg, nil
	}
	if f.nextErr != nil {
		return RPCMessage{}, f.nextErr
	}
	return RPCMessage{}, io.EOF
}

func (f *fakeRPCClient) Close() error {
	f.closed = true
	return nil
}

func TestRunAppFallbackBootstrapsThreadAndTurn(t *testing.T) {
	rpc := &fakeRPCClient{
		next: []RPCMessage{
			{ID: rpcIDPtr(1), Result: json.RawMessage(`{"userAgent":"codex","codexHome":"/tmp/codex","platformFamily":"unix","platformOs":"macos"}`)},
			{ID: rpcIDPtr(2), Result: json.RawMessage(`{"thread":{"id":"thread-123"},"model":"gpt-5.4"}`)},
			{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"hello "}`)},
			{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"world"}`)},
			{Method: "turn/completed", Params: json.RawMessage(`{}`)},
		},
	}

	var chunks []adapteropenai.StreamChunk
	res, err := RunAppFallback(context.Background(), AppFallbackConfig{
		Binary:         "codex",
		RequestID:      "req-1",
		Model:          "gpt-5.4",
		Effort:         "medium",
		Summary:        "auto",
		SystemPrompt:   "sys",
		Prompt:         "prompt",
		SanitizePrompt: func(s string) string { return s },
		StartRPC: func(context.Context, string, map[string]string) (RPCClient, error) {
			return rpc, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunAppFallback: %v", err)
	}
	if res.FinishReason != "stop" {
		t.Fatalf("finish_reason=%q want stop", res.FinishReason)
	}
	if len(rpc.sent) < 5 {
		t.Fatalf("expected initialize/thread-start/turn-start/archive calls, got %d", len(rpc.sent))
	}
	if rpc.sent[0].method != "initialize" || rpc.sent[1].method != "initialized" || rpc.sent[2].method != "thread/start" || rpc.sent[3].method != "turn/start" {
		t.Fatalf("unexpected rpc call sequence: %+v", rpc.sent)
	}
	if !rpc.closed {
		t.Fatalf("expected RPC client to be closed")
	}
	if len(chunks) == 0 {
		t.Fatalf("expected assistant chunks")
	}
}

func TestRunAppFallbackReturnsRPCError(t *testing.T) {
	startErr := errors.New("spawn failed")
	_, err := RunAppFallback(context.Background(), AppFallbackConfig{
		Binary:    "codex",
		RequestID: "req-1",
		StartRPC: func(context.Context, string, map[string]string) (RPCClient, error) {
			return nil, startErr
		},
	}, func(adapteropenai.StreamChunk) error { return nil })
	if !errors.Is(err, startErr) {
		t.Fatalf("err=%v want spawn failed", err)
	}
}

func TestRunManagedTurnTracksAssistantAndCache(t *testing.T) {
	transport := &fakeAppTurnTransport{
		threadID: "thread-123",
		cached:   100,
		next: []RPCMessage{
			{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"hello "}`)},
			{Method: "item/agentMessage/delta", Params: json.RawMessage(`{"delta":"world"}`)},
			{Method: "thread/tokenUsage/updated", Params: json.RawMessage(`{"tokenUsage":{"last":{"totalTokens":14,"inputTokens":10,"cachedInputTokens":175,"outputTokens":4,"reasoningOutputTokens":0}}}`)},
			{Method: "turn/completed", Params: json.RawMessage(`{}`)},
		},
	}
	var chunks []adapteropenai.StreamChunk
	res, assistant, err := RunManagedTurn(context.Background(), transport, AppTurnConfig{
		RequestID: "req-managed",
		Model:     "gpt-5.4",
		Effort:    "medium",
		Summary:   "auto",
		Prompt:    "prompt",
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}, func(ch adapteropenai.StreamChunk) error {
		chunks = append(chunks, ch)
		return nil
	})
	if err != nil {
		t.Fatalf("RunManagedTurn: %v", err)
	}
	if assistant != "hello world" {
		t.Fatalf("assistant=%q want hello world", assistant)
	}
	if res.DerivedCacheCreationTokens != 75 {
		t.Fatalf("derived_cache_creation_tokens=%d want 75", res.DerivedCacheCreationTokens)
	}
	if transport.cached != 175 {
		t.Fatalf("cached=%d want 175", transport.cached)
	}
	if len(chunks) == 0 {
		t.Fatalf("expected chunks")
	}
}

type fakeAppTurnTransport struct {
	sent     []rpcCall
	next     []RPCMessage
	threadID string
	cached   int
}

func (f *fakeAppTurnTransport) SendTurnStart(id int, params RPCTurnStartParams) error {
	f.sent = append(f.sent, rpcCall{id: id, method: params.rpcMethod(), params: params})
	return nil
}

func (f *fakeAppTurnTransport) Next() (RPCMessage, error) {
	if len(f.next) == 0 {
		return RPCMessage{}, io.EOF
	}
	msg := f.next[0]
	f.next = f.next[1:]
	return msg, nil
}

func (f *fakeAppTurnTransport) ThreadID() string { return f.threadID }

func (f *fakeAppTurnTransport) CachedInputTokens() int { return f.cached }

func (f *fakeAppTurnTransport) SetCachedInputTokens(v int) { f.cached = v }

func rpcIDPtr(id int) *RPCID {
	out := NewRPCID(id)
	return &out
}
