package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

type AppFallbackConfig struct {
	Binary         string
	RequestID      string
	Model          string
	Effort         ReasoningEffort
	Summary        ReasoningSummary
	SystemPrompt   string
	Prompt         string
	SanitizePrompt func(string) string
	StartRPC       RPCStarter
	StartRPCEnv    map[string]string
	Logger         *slog.Logger
	BodyLog        BodyLogConfig
}

// AppTurnTransport is the narrow JSON-RPC surface RunManagedTurn uses
// to drive a Codex app session.
type AppTurnTransport interface {
	SendTurnStart(id int, params RPCTurnStartParams) error
	Next() (RPCMessage, error)
	ThreadID() string
	CachedInputTokens() int
	SetCachedInputTokens(int)
}

type AppTurnConfig struct {
	RequestID      string
	Model          string
	Effort         ReasoningEffort
	Summary        ReasoningSummary
	Prompt         string
	SanitizePrompt func(string) string
	Logger         *slog.Logger
	BodyLog        BodyLogConfig
}

func RunManagedTurn(
	ctx context.Context,
	transport AppTurnTransport,
	cfg AppTurnConfig,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, string, error) {
	prompt := strings.TrimSpace(cfg.Prompt)
	if prompt == "" {
		prompt = " "
	}
	sanitize := cfg.SanitizePrompt
	if sanitize == nil {
		sanitize = SanitizeForUpstreamCache
	}
	turnParams := RPCTurnStartParams{
		ThreadID:       transport.ThreadID(),
		ApprovalPolicy: AskForApprovalNever,
		Model:          cfg.Model,
		Effort:         cfg.Effort,
		Summary:        cfg.Summary,
		Input:          []RPCTurnInputItem{NewTextInput(sanitize(prompt))},
	}
	logAppRPCRequest(ctx, cfg.RequestID, cfg.Model, "turn/start", turnParams, cfg.BodyLog)
	if err := transport.SendTurnStart(3, turnParams); err != nil {
		return NewRunResult("stop"), "", err
	}

	out := NewRunResult("stop")
	var assistantText strings.Builder
	renderer := adapterrender.NewEventRenderer(cfg.RequestID, cfg.Model, "codex", cfg.Logger)
	for {
		select {
		case <-ctx.Done():
			return out, assistantText.String(), ctx.Err()
		default:
		}
		msg, err := transport.Next()
		if err != nil {
			return out, assistantText.String(), err
		}
		logAppRPCResponse(ctx, cfg.RequestID, cfg.Model, msg, cfg.BodyLog)
		if msg.ID != nil && RPCIDEquals(msg.ID, 3) {
			if msg.Error != nil {
				return out, assistantText.String(), fmt.Errorf("codex turn/start: %s", msg.Error.Message)
			}
			continue
		}
		LogProtocolEvent(ctx, cfg.RequestID, "codex", msg.Method, slog.Int("params_bytes", len(msg.Params)))
		switch msg.Method {
		case "item/agentMessage/delta":
			var p RPCAgentMessageDeltaNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("delta_len", len(p.Delta)))
			if p.Delta != "" {
				if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventAssistantTextDelta, Text: p.Delta}, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "turn/plan/updated":
			var p RPCTurnPlanUpdatedNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("plan_steps", len(p.Plan)), slog.Bool("has_explanation", strings.TrimSpace(p.Explanation) != ""))
			if ev, ok := PlanEvent(p.Explanation, p.Plan); ok {
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/started", "item/completed":
			var p RPCItemNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_type", p.Item.ItemType()), slog.String("item_status", p.Item.ItemStatus()))
			if ev, ok := LifecycleEvent(p.Item, msg.Method == "item/completed"); ok {
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/commandExecution/outputDelta", "item/fileChange/outputDelta":
			var p RPCOutputDeltaNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("delta_len", len(p.Delta)))
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, p.Delta); ok {
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/mcpToolCall/progress":
			var p RPCMcpToolCallProgressNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("message_len", len(p.Message)))
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, p.Message); ok {
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/fileChange/patchUpdated":
			var p RPCFileChangePatchUpdatedNotification
			_ = json.Unmarshal(msg.Params, &p)
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.String("item_id", p.ItemID), slog.Int("change_count", len(p.Changes)))
			changeCount := len(p.Changes)
			if changeCount < 1 {
				changeCount = 1
			}
			if ev, ok := ProgressEvent(msg.Method, p.ItemID, fmt.Sprintf("Patch updated for %d file(s)", changeCount)); ok {
				ev.ChangeCount = changeCount
				if err := EmitRendered(renderer, ev, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "item/reasoning/summaryPartAdded":
			var p RPCReasoningSummaryPartAddedNotification
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Bool("thinking_visible", renderer.State().ReasoningVisible))
			if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventReasoningSignaled}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
		case "item/reasoning/summaryTextDelta", "item/reasoning/textDelta":
			var p RPCReasoningTextDeltaNotification
			_ = json.Unmarshal(msg.Params, &p)
			out.ReasoningSignaled = true
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Int("summary_index", p.SummaryIndex), slog.Int("delta_len", len(p.Delta)), slog.Bool("thinking_visible_before", renderer.State().ReasoningVisible))
			if p.Delta != "" {
				kind := "text"
				var summaryIdx *int
				if msg.Method == "item/reasoning/summaryTextDelta" {
					kind = "summary"
					summaryIdx = &p.SummaryIndex
				}
				if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventReasoningDelta, Text: p.Delta, ReasoningKind: kind, SummaryIndex: summaryIdx}, emit, &assistantText); err != nil {
					return out, assistantText.String(), err
				}
			}
		case "thread/tokenUsage/updated":
			var p RPCThreadTokenUsageUpdatedNotification
			_ = json.Unmarshal(msg.Params, &p)
			currentCached := p.TokenUsage.Last.CachedInputTokens
			derivedCacheCreate := DeriveCacheCreationTokens(transport.CachedInputTokens(), currentCached)
			logAttrs := []slog.Attr{slog.Int("prompt_tokens", p.TokenUsage.Last.InputTokens), slog.Int("completion_tokens", p.TokenUsage.Last.OutputTokens), slog.Int("cached_input_tokens", currentCached), slog.Int("derived_cache_creation_tokens", derivedCacheCreate), slog.Int("reasoning_output_tokens", p.TokenUsage.Last.ReasoningOutputTokens), slog.Bool("native_cache_creation_metric_available", false)}
			LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, logAttrs...)
			out.Usage.PromptTokens = p.TokenUsage.Last.InputTokens
			out.Usage.CompletionTokens = p.TokenUsage.Last.OutputTokens
			out.Usage.TotalTokens = p.TokenUsage.Last.TotalTokens
			if currentCached > 0 {
				out.Usage.PromptTokensDetails = &adapteropenai.PromptTokensDetails{CachedTokens: currentCached}
			}
			out.DerivedCacheCreationTokens = derivedCacheCreate
			transport.SetCachedInputTokens(currentCached)
			if p.TokenUsage.Last.ReasoningOutputTokens > 0 {
				out.ReasoningSignaled = true
			}
		case "turn/completed":
			if err := EmitRendered(renderer, adapterrender.Event{Kind: adapterrender.EventReasoningFinished}, emit, &assistantText); err != nil {
				return out, assistantText.String(), err
			}
			state := renderer.State()
			out.ReasoningSignaled = out.ReasoningSignaled || state.ReasoningSignaled
			out.ReasoningVisible = state.ReasoningVisible
			LogReasoningEvent(cfg.Logger, ctx, cfg.RequestID, msg.Method, slog.Bool("reasoning_signaled", out.ReasoningSignaled), slog.Bool("thinking_visible", out.ReasoningVisible))
			return out, assistantText.String(), nil
		default:
			if strings.HasPrefix(msg.Method, "item/") || strings.HasPrefix(msg.Method, "thread/") || strings.HasPrefix(msg.Method, "turn/") {
				LogToolingEvent(cfg.Logger, ctx, cfg.RequestID, "ignored", slog.String("method", msg.Method), slog.Int("params_bytes", len(msg.Params)))
			}
		}
	}
}

type rpcAppTurnTransport struct {
	rpc      RPCClient
	threadID string
	cached   int
}

func (t *rpcAppTurnTransport) SendTurnStart(id int, params RPCTurnStartParams) error {
	return t.rpc.SendTurnStart(id, params)
}

func (t *rpcAppTurnTransport) Next() (RPCMessage, error) { return t.rpc.Next() }

func (t *rpcAppTurnTransport) ThreadID() string { return t.threadID }

func (t *rpcAppTurnTransport) CachedInputTokens() int { return t.cached }

func (t *rpcAppTurnTransport) SetCachedInputTokens(v int) { t.cached = v }

func RunAppFallback(ctx context.Context, cfg AppFallbackConfig, emit func(adapteropenai.StreamChunk) error) (RunResult, error) {
	rpc, err := cfg.StartRPC(ctx, cfg.Binary, cfg.StartRPCEnv)
	if err != nil {
		return NewRunResult("stop"), err
	}
	defer func() { _ = rpc.Close() }()

	waitFor := func(id int) (RPCMessage, error) {
		for {
			msg, err := rpc.Next()
			if err != nil {
				return RPCMessage{}, err
			}
			logAppRPCResponse(ctx, cfg.RequestID, cfg.Model, msg, cfg.BodyLog)
			if msg.ID == nil || !RPCIDEquals(msg.ID, id) {
				continue
			}
			if msg.Error != nil {
				return RPCMessage{}, fmt.Errorf("codex rpc %s", strings.TrimSpace(msg.Error.Message))
			}
			return msg, nil
		}
	}

	initParams := RPCInitializeParams{
		ClientInfo: RPCClientInfo{
			Name:    "clyde-adapter",
			Title:   "Clyde Adapter",
			Version: "0.1.0",
		},
	}
	logAppRPCRequest(ctx, cfg.RequestID, cfg.Model, "initialize", initParams, cfg.BodyLog)
	if err := rpc.SendInitialize(1, initParams); err != nil {
		return NewRunResult("stop"), err
	}
	if _, err := waitFor(1); err != nil {
		return NewRunResult("stop"), err
	}
	if err := rpc.NotifyInitialized(); err != nil {
		return NewRunResult("stop"), err
	}

	threadID := ""
	threadParams := RPCThreadStartParams{
		Cwd:                    ".",
		ApprovalPolicy:         AskForApprovalNever,
		Ephemeral:              true,
		Model:                  strings.TrimSpace(cfg.Model),
		BaseInstructions:       strings.TrimSpace(cfg.SystemPrompt),
		ServiceName:            "clyde-codex-session",
		ExperimentalRawEvents:  false,
		PersistExtendedHistory: false,
	}
	logAppRPCRequest(ctx, cfg.RequestID, cfg.Model, "thread/start", threadParams, cfg.BodyLog)
	if err := rpc.SendThreadStart(2, threadParams); err != nil {
		return NewRunResult("stop"), err
	}
	threadMsg, err := waitFor(2)
	if err != nil {
		return NewRunResult("stop"), err
	}
	if len(threadMsg.Result) > 0 {
		var r RPCThreadStartResponse
		_ = json.Unmarshal(threadMsg.Result, &r)
		threadID = strings.TrimSpace(r.Thread.ID)
	}
	defer func() {
		if threadID == "" {
			return
		}
		archiveParams := RPCThreadArchiveParams{ThreadID: threadID}
		logAppRPCRequest(ctx, cfg.RequestID, cfg.Model, "thread/archive", archiveParams, cfg.BodyLog)
		_ = rpc.SendThreadArchive(9, archiveParams)
	}()

	out, _, err := RunManagedTurn(ctx, &rpcAppTurnTransport{rpc: rpc, threadID: threadID}, AppTurnConfig{
		RequestID:      cfg.RequestID,
		Model:          cfg.Model,
		Effort:         cfg.Effort,
		Summary:        cfg.Summary,
		Prompt:         cfg.Prompt,
		SanitizePrompt: cfg.SanitizePrompt,
		Logger:         cfg.Logger,
		BodyLog:        cfg.BodyLog,
	}, emit)
	return out, err
}

// logAppRPCRequest is the codex.app.request equivalent of the
// websocket/HTTP wire-body log. The typed struct is marshalled to JSON
// and then run through the defensive secret-key redactor before the
// payload is recorded. Callers should always pass one of the
// RPC*Params structs from app_protocol.go.
func logAppRPCRequest(ctx context.Context, requestID, model, method string, params rpcRequestParams, bodyLog BodyLogConfig) {
	mode, maxBytes := bodyLog.Resolve()
	if mode == BodyLogOff {
		return
	}
	raw, err := json.Marshal(params)
	if err != nil {
		raw = []byte(fmt.Sprintf("%v", params))
	}
	redacted := redactedRPCParams(raw)
	ev := requestEvent{
		Subcomponent: "codex",
		Transport:    "app_rpc",
		Method:       method,
		RequestID:    requestID,
		Model:        model,
		BodyBytes:    len(redacted),
	}
	body, b64, truncated := applyBodyMode(redacted, mode, maxBytes)
	ev.Body = body
	ev.BodyB64 = b64
	ev.BodyTruncated = truncated
	logCodexEvent(ctx, slog.LevelDebug, "codex.app.request", ev.toSlogAttrs())
}

func logAppRPCResponse(ctx context.Context, requestID, model string, msg RPCMessage, bodyLog BodyLogConfig) {
	mode, maxBytes := bodyLog.Resolve()
	if mode == BodyLogOff {
		return
	}
	raw := msg.Result
	if len(raw) == 0 {
		raw = msg.Params
	}
	if msg.Error != nil {
		errorBody, err := json.Marshal(msg.Error)
		if err == nil {
			raw = errorBody
		}
	}
	redacted := redactedRPCParams(raw)
	method := msg.Method
	if method == "" && msg.ID != nil {
		method = "rpc.response." + msg.ID.IntString()
	}
	ev := responseEventCodex{
		Subcomponent: "codex",
		Transport:    "app_rpc",
		Method:       method,
		RequestID:    requestID,
		Model:        model,
		BodyBytes:    len(redacted),
	}
	if msg.Error != nil {
		ev.Err = msg.Error.Message
	}
	body, b64, truncated := applyBodyMode(redacted, mode, maxBytes)
	ev.Body = body
	ev.BodyB64 = b64
	ev.BodyTruncated = truncated
	logCodexEvent(ctx, slog.LevelDebug, "codex.app.response", ev.toSlogAttrs())
}
