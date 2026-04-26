package adapter

import (
	"context"
	"log/slog"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adaptercursor "goodkind.io/clyde/internal/adapter/cursor"
	"goodkind.io/clyde/internal/adapter/tooltrans"
)

type codexManagedPromptPlan = adaptercodex.ManagedPromptPlan
type codexSessionSpec = adaptercodex.SessionSpec
type codexSessionAcquireResult = adaptercodex.SessionAcquireResult
type codexManagedTransport = adaptercodex.ManagedTransport
type codexManagedSession = adaptercodex.ManagedSession
type codexSessionManager = adaptercodex.SessionManager

func buildCodexManagedPromptPlan(messages []ChatMessage) codexManagedPromptPlan {
	return adaptercodex.BuildManagedPromptPlan(messages, BuildPrompt, FlattenContent, sanitizeForUpstreamCache)
}

func newCodexSessionManager(log *slog.Logger, start func(spec codexSessionSpec) (codexManagedTransport, error)) *codexSessionManager {
	return adaptercodex.NewSessionManager(log, start)
}

func newCodexAppTransport(bin string, spec codexSessionSpec) (*adaptercodex.AppTransport, error) {
	return adaptercodex.NewAppTransport(bin, spec, adaptercodex.StartRPC)
}

func (s *Server) codexCursorContext(req ChatRequest) adaptercursor.Context {
	return adaptercursor.TranslateRequest(req).Context()
}

func (s *Server) runCodexManaged(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(tooltrans.OpenAIStreamChunk) error,
) (codexRunResult, string, bool, error) {
	out, err := adaptercodex.RunManagedSession(
		s.log,
		ctx,
		s.codexSessions,
		req,
		s.codexCursorContext(req),
		model,
		effort,
		buildCodexManagedPromptPlan,
		reqID,
		emit,
	)
	if err != nil {
		return codexRunResult{}, "", out.Managed, err
	}
	res, _ := out.Result.(codexRunResult)
	return res, out.AssistantText, out.Managed, nil
}
