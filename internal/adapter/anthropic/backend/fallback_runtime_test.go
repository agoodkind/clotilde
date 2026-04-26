package anthropicbackend

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic/fallback"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

type fakeFallbackClient struct {
	collect func(context.Context, fallback.Request, fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error)
	stream  func(context.Context, fallback.Request, fallback.StreamOpenAIInput, func(adapteropenai.StreamChunk) error) (fallback.StreamOpenAIResult, error)
}

func (c *fakeFallbackClient) CollectOpenAI(ctx context.Context, req fallback.Request, in fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error) {
	if c.collect != nil {
		return c.collect(ctx, req, in)
	}
	return fallback.CollectOpenAIResult{}, nil
}

func (c *fakeFallbackClient) StreamOpenAI(ctx context.Context, req fallback.Request, in fallback.StreamOpenAIInput, emit func(adapteropenai.StreamChunk) error) (fallback.StreamOpenAIResult, error) {
	if c.stream != nil {
		return c.stream(ctx, req, in, emit)
	}
	return fallback.StreamOpenAIResult{}, nil
}

type fakeFallbackDispatcher struct {
	fakeResponseDispatcher
	fb              *fakeFallbackClient
	acquireErr      error
	acquireCount    int
	releaseCount    int
	streamEnabled   bool
	dropUnsupported bool
	transcriptOn    bool
	workspaceDir    string
	jsonPrompt      string
	writeStatus     int
	writeValue      any
	writeErrorCode  string
	writeErrorMsg   string
	terminalEvents  []adapterruntime.RequestEvent
	cacheLogBackend string
	cacheLogPrompt  int
	cacheLogCreate  int
	cacheLogRead    int
}

func (d *fakeFallbackDispatcher) FallbackClient() FallbackClient {
	return d.fb
}

func (d *fakeFallbackDispatcher) AcquireFallback(context.Context) error {
	d.acquireCount++
	return d.acquireErr
}

func (d *fakeFallbackDispatcher) ReleaseFallback() {
	d.releaseCount++
}

func (d *fakeFallbackDispatcher) ParseResponseFormat(raw any) any {
	return raw
}

func (d *fakeFallbackDispatcher) FallbackJSONSystemPrompt(any) string {
	return d.jsonPrompt
}

func (d *fakeFallbackDispatcher) FallbackStreamPassthrough() bool {
	return d.streamEnabled
}

func (d *fakeFallbackDispatcher) FallbackDropUnsupported() bool {
	return d.dropUnsupported
}

func (d *fakeFallbackDispatcher) FallbackTranscriptSynthesisEnabled() bool {
	return d.transcriptOn
}

func (d *fakeFallbackDispatcher) FallbackTranscriptWorkspaceDir(string) string {
	return d.workspaceDir
}

func (d *fakeFallbackDispatcher) WriteJSON(_ http.ResponseWriter, status int, v any) {
	d.writeStatus = status
	d.writeValue = v
}

func (d *fakeFallbackDispatcher) WriteError(_ http.ResponseWriter, status int, code, msg string) {
	d.writeStatus = status
	d.writeErrorCode = code
	d.writeErrorMsg = msg
}

func (d *fakeFallbackDispatcher) LogTerminal(_ context.Context, ev adapterruntime.RequestEvent) {
	d.terminalEvents = append(d.terminalEvents, ev)
}

func (d *fakeFallbackDispatcher) LogCacheUsageFallback(_ context.Context, backend, _ string, _ string, promptTokens, cacheCreationTokens, cacheReadTokens int) {
	d.cacheLogBackend = backend
	d.cacheLogPrompt = promptTokens
	d.cacheLogCreate = cacheCreationTokens
	d.cacheLogRead = cacheReadTokens
}

func TestCollectFallbackResponseWritesFinalAndTerminalEvents(t *testing.T) {
	t.Parallel()
	dispatcher := &fakeFallbackDispatcher{}
	dispatcher.fb = &fakeFallbackClient{
		collect: func(_ context.Context, req fallback.Request, in fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error) {
			if in.SystemFingerprint != "fp-test" {
				t.Fatalf("SystemFingerprint = %q", in.SystemFingerprint)
			}
			raw := fallback.Result{
				Text: "ok",
				Usage: fallback.Usage{
					PromptTokens:             10,
					CompletionTokens:         2,
					TotalTokens:              12,
					CacheCreationInputTokens: 3,
					CacheReadInputTokens:     4,
				},
				Stop: "end_turn",
			}
			return fallback.CollectOpenAIResult{
				Raw: raw,
				Final: fallback.BuildFinalResponse(fallback.FinalResponseInput{
					Request:           req,
					Result:            raw,
					RequestID:         in.RequestID,
					ModelAlias:        in.ModelAlias,
					SystemFingerprint: in.SystemFingerprint,
					CoerceText:        in.CoerceText,
				}),
			}, nil
		},
	}
	req := fallback.Request{Model: "sonnet", SessionID: "session-1"}
	model := adaptermodel.ResolvedModel{Alias: "alias", Backend: adaptermodel.BackendFallback}
	err := CollectFallbackResponse(dispatcher, nil, context.Background(), req, model, "req-1", time.Now(), nil, false)
	if err != nil {
		t.Fatalf("CollectFallbackResponse: %v", err)
	}
	if dispatcher.writeStatus != http.StatusOK {
		t.Fatalf("writeStatus = %d", dispatcher.writeStatus)
	}
	if dispatcher.writeValue == nil {
		t.Fatalf("expected response body")
	}
	if dispatcher.cacheLogBackend != "fallback" || dispatcher.cacheLogPrompt != 10 || dispatcher.cacheLogCreate != 3 || dispatcher.cacheLogRead != 4 {
		t.Fatalf("cache log = backend %q prompt %d create %d read %d", dispatcher.cacheLogBackend, dispatcher.cacheLogPrompt, dispatcher.cacheLogCreate, dispatcher.cacheLogRead)
	}
	if len(dispatcher.terminalEvents) != 1 || dispatcher.terminalEvents[0].Stage != adapterruntime.RequestStageCompleted {
		t.Fatalf("terminal events = %+v", dispatcher.terminalEvents)
	}
}

func TestCollectFallbackResponseEscalatesErrorsWithoutWriting(t *testing.T) {
	t.Parallel()
	upstreamErr := errors.New("fallback failed")
	dispatcher := &fakeFallbackDispatcher{fb: &fakeFallbackClient{
		collect: func(context.Context, fallback.Request, fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error) {
			return fallback.CollectOpenAIResult{}, upstreamErr
		},
	}}
	req := fallback.Request{Model: "sonnet"}
	model := adaptermodel.ResolvedModel{Alias: "alias", Backend: adaptermodel.BackendFallback}
	err := CollectFallbackResponse(dispatcher, nil, context.Background(), req, model, "req-1", time.Now(), nil, true)
	if !errors.Is(err, upstreamErr) {
		t.Fatalf("err = %v want %v", err, upstreamErr)
	}
	if dispatcher.writeStatus != 0 {
		t.Fatalf("unexpected write status %d", dispatcher.writeStatus)
	}
	if len(dispatcher.terminalEvents) != 1 || dispatcher.terminalEvents[0].Stage != adapterruntime.RequestStageFailed {
		t.Fatalf("terminal events = %+v", dispatcher.terminalEvents)
	}
}

func TestStreamFallbackResponseWritesChunksDoneAndTerminalEvents(t *testing.T) {
	t.Parallel()
	dispatcher := &fakeFallbackDispatcher{}
	dispatcher.fb = &fakeFallbackClient{
		stream: func(_ context.Context, req fallback.Request, in fallback.StreamOpenAIInput, emit func(adapteropenai.StreamChunk) error) (fallback.StreamOpenAIResult, error) {
			if err := emit(adapterruntime.BuildDeltaChunk(in.RequestID, in.ModelAlias, in.Created, adapteropenai.StreamDelta{Role: "assistant", Content: "hi"})); err != nil {
				return fallback.StreamOpenAIResult{}, err
			}
			raw := fallback.StreamResult{
				Text:  "hi",
				Stop:  "end_turn",
				Usage: fallback.Usage{PromptTokens: 7, CompletionTokens: 1, TotalTokens: 8},
			}
			return fallback.StreamOpenAIResult{
				Raw: raw,
				Plan: fallback.BuildStreamPlan(fallback.StreamPlanInput{
					Request:    req,
					Result:     raw,
					RequestID:  in.RequestID,
					ModelAlias: in.ModelAlias,
					Created:    in.Created,
				}),
			}, nil
		},
	}
	req := fallback.Request{Model: "sonnet", ToolChoice: "none"}
	model := adaptermodel.ResolvedModel{Alias: "alias", Backend: adaptermodel.BackendFallback}
	err := StreamFallbackResponse(dispatcher, nil, &http.Request{}, req, model, "req-1", time.Now(), false, true)
	if err != nil {
		t.Fatalf("StreamFallbackResponse: %v", err)
	}
	if dispatcher.sseWriter == nil || dispatcher.sseWriter.doneCount != 1 {
		t.Fatalf("sse writer = %+v", dispatcher.sseWriter)
	}
	if len(dispatcher.sseWriter.chunks) != 3 {
		t.Fatalf("chunks len = %d chunks = %+v", len(dispatcher.sseWriter.chunks), dispatcher.sseWriter.chunks)
	}
	if dispatcher.sseWriter.chunks[0].Choices[0].Delta.Content != "hi" {
		t.Fatalf("first chunk = %+v", dispatcher.sseWriter.chunks[0])
	}
	if dispatcher.sseWriter.chunks[1].Choices[0].FinishReason == nil || *dispatcher.sseWriter.chunks[1].Choices[0].FinishReason != "stop" {
		t.Fatalf("finish chunk = %+v", dispatcher.sseWriter.chunks[1])
	}
	if dispatcher.sseWriter.chunks[2].Usage == nil || dispatcher.sseWriter.chunks[2].Usage.TotalTokens != 8 {
		t.Fatalf("usage chunk = %+v", dispatcher.sseWriter.chunks[2])
	}
	if len(dispatcher.terminalEvents) != 1 || dispatcher.terminalEvents[0].Stage != adapterruntime.RequestStageCompleted {
		t.Fatalf("terminal events = %+v", dispatcher.terminalEvents)
	}
}

func TestHandleFallbackBuildsRequestAndAppliesJSONPrompt(t *testing.T) {
	t.Parallel()
	dispatcher := &fakeFallbackDispatcher{
		streamEnabled: true,
		jsonPrompt:    "Return JSON only.",
	}
	dispatcher.fb = &fakeFallbackClient{
		collect: func(_ context.Context, req fallback.Request, in fallback.CollectOpenAIInput) (fallback.CollectOpenAIResult, error) {
			if req.Model != "sonnet" {
				t.Fatalf("Model = %q", req.Model)
			}
			if req.System != "system\n\nReturn JSON only." {
				t.Fatalf("System = %q", req.System)
			}
			if len(req.Tools) != 1 || req.Tools[0].Name != "Read" {
				t.Fatalf("Tools = %+v", req.Tools)
			}
			if req.ToolChoice != "Read" {
				t.Fatalf("ToolChoice = %q", req.ToolChoice)
			}
			raw := fallback.Result{Text: "{}", Usage: fallback.Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}, Stop: "end_turn"}
			return fallback.CollectOpenAIResult{
				Raw: raw,
				Final: fallback.BuildFinalResponse(fallback.FinalResponseInput{
					Request:           req,
					Result:            raw,
					RequestID:         in.RequestID,
					ModelAlias:        in.ModelAlias,
					SystemFingerprint: in.SystemFingerprint,
					CoerceText:        in.CoerceText,
				}),
			}, nil
		},
	}
	req := adapteropenai.ChatRequest{
		Messages: []adapteropenai.ChatMessage{
			{Role: "system", Content: mustRawMessage(`"system"`)},
			{Role: "user", Content: mustRawMessage(`"hi"`)},
		},
		Tools:      []adapteropenai.Tool{{Function: adapteropenai.ToolFunctionSchema{Name: "Read"}}},
		ToolChoice: mustRawMessage(`{"type":"function","function":{"name":"Read"}}`),
	}
	model := adaptermodel.ResolvedModel{
		Alias:      "alias",
		Backend:    adaptermodel.BackendFallback,
		FamilySlug: "sonnet-family",
		CLIAlias:   "sonnet",
	}
	err := HandleFallback(dispatcher, nil, &http.Request{}, req, model, "req-1", false)
	if err != nil {
		t.Fatalf("HandleFallback: %v", err)
	}
	if dispatcher.acquireCount != 1 || dispatcher.releaseCount != 1 {
		t.Fatalf("acquire/release = %d/%d", dispatcher.acquireCount, dispatcher.releaseCount)
	}
	if dispatcher.writeStatus != http.StatusOK {
		t.Fatalf("writeStatus = %d", dispatcher.writeStatus)
	}
}

func TestHandleFallbackRejectsMissingCLIAlias(t *testing.T) {
	t.Parallel()
	dispatcher := &fakeFallbackDispatcher{fb: &fakeFallbackClient{}, streamEnabled: true}
	model := adaptermodel.ResolvedModel{
		Alias:      "alias",
		Backend:    adaptermodel.BackendFallback,
		FamilySlug: "family",
	}
	err := HandleFallback(dispatcher, nil, &http.Request{}, adapteropenai.ChatRequest{}, model, "req-1", true)
	if err == nil {
		t.Fatalf("expected escalated error, got %v", err)
	}
	if dispatcher.acquireCount != 0 {
		t.Fatalf("unexpected acquire count %d", dispatcher.acquireCount)
	}
	if dispatcher.writeStatus != 0 {
		t.Fatalf("unexpected write status %d", dispatcher.writeStatus)
	}
}

func mustRawMessage(s string) json.RawMessage {
	return json.RawMessage(s)
}
