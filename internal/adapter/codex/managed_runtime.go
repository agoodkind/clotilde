package codex

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type ManagedRuntime interface {
	Log() *slog.Logger
	NormalizeAssistantAnchor(string) string
	ManagedSummary(adapteropenai.ChatRequest) string
	EffectiveAppEffort(adapteropenai.ChatRequest) any
	EffectiveAppSummary(adapteropenai.ChatRequest) any
	RunManagedTurn(context.Context, *ManagedSession, SessionSpec, string, string, any, any, func(tooltrans.OpenAIStreamChunk) error) (any, string, error)
}

type ManagedRunResult struct {
	Result        any
	AssistantText string
	Managed       bool
}

func RunManagedSession(
	rt ManagedRuntime,
	ctx context.Context,
	manager *SessionManager,
	req adapteropenai.ChatRequest,
	cursor adaptercursor.Context,
	model adaptermodel.ResolvedModel,
	effort string,
	buildPlan func([]adapteropenai.ChatMessage) ManagedPromptPlan,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (ManagedRunResult, error) {
	if manager == nil {
		return ManagedRunResult{}, nil
	}
	sessionKey := cursor.StrongConversationKey()
	if sessionKey == "" {
		rt.Log().LogAttrs(ctx, slog.LevelInfo, "adapter.codex.session.not_admitted",
			slog.String("request_id", reqID),
			slog.String("reason", "missing_cursor_conversation_id"),
			slog.String("cursor_request_id", cursor.RequestID),
		)
		return ManagedRunResult{}, nil
	}

	plan := buildPlan(req.Messages)
	spec := SessionSpec{
		Key:     sessionKey,
		Model:   strings.TrimSpace(model.ClaudeModel),
		Effort:  strings.ToLower(strings.TrimSpace(effort)),
		Summary: strings.ToLower(strings.TrimSpace(rt.ManagedSummary(req))),
		System:  plan.System,
	}
	if spec.Model == "" {
		spec.Model = strings.TrimSpace(model.Alias)
	}

	acquired, err := manager.Acquire(ctx, spec)
	if err != nil {
		return ManagedRunResult{}, err
	}
	session := acquired.Session
	defer manager.Release(session)

	prompt := plan.IncrementalPrompt
	promptMode := "incremental"
	resetReason := acquired.ResetReason
	if acquired.Created {
		prompt = plan.FullPrompt
		promptMode = "full"
	}

	if !acquired.Created {
		switch {
		case session.LastAssistant == "" && plan.AssistantAnchor != "":
			resetReason = "assistant_anchor_missing"
		case session.LastAssistant != "" && plan.AssistantAnchor == "":
			resetReason = "assistant_anchor_dropped"
		case session.LastAssistant != "" && plan.AssistantAnchor != "" && session.LastAssistant != plan.AssistantAnchor:
			resetReason = "assistant_anchor_mismatch"
		}
		if resetReason != "" {
			manager.Drop(session, resetReason)
			acquired, err = manager.Acquire(ctx, spec)
			if err != nil {
				return ManagedRunResult{}, err
			}
			session = acquired.Session
			defer manager.Release(session)
			prompt = plan.FullPrompt
			promptMode = "full"
		}
	}

	rt.Log().LogAttrs(ctx, slog.LevelInfo, "adapter.codex.session.admitted",
		slog.String("request_id", reqID),
		slog.String("session_key", sessionKey),
		slog.String("cursor_conversation_id", cursor.ConversationID),
		slog.String("cursor_request_id", cursor.RequestID),
		slog.Bool("created", acquired.Created),
		slog.String("prompt_mode", promptMode),
	)

	session.RunMu.Lock()
	defer session.RunMu.Unlock()
	res, assistantText, err := rt.RunManagedTurn(
		ctx,
		session,
		spec,
		reqID,
		prompt,
		rt.EffectiveAppEffort(req),
		rt.EffectiveAppSummary(req),
		emit,
	)
	if err != nil {
		manager.Drop(session, "transport_error")
		return ManagedRunResult{}, err
	}
	session.LastAssistant = rt.NormalizeAssistantAnchor(assistantText)
	return ManagedRunResult{Result: res, AssistantText: assistantText, Managed: true}, nil
}

func ManagedTransportTypeMismatch() error {
	return fmt.Errorf("codex session transport type mismatch")
}
