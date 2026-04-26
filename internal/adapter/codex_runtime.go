package adapter

import (
	"context"
	"os"
	"strings"
	"time"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type codexRunResult = adaptercodex.RunResult

var (
	codexNow       = time.Now
	codexGetwd     = os.Getwd
	codexShellName = func() string {
		shell := strings.TrimSpace(os.Getenv("SHELL"))
		if shell == "" {
			return "sh"
		}
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}
)

func init() {
	adaptercodex.NowFunc = func() time.Time { return codexNow() }
	adaptercodex.GetwdFn = func() (string, error) { return codexGetwd() }
	adaptercodex.ShellNameFn = func() string { return codexShellName() }
}

func sanitizeForUpstreamCache(text string) string {
	return adaptercodex.SanitizeForUpstreamCache(text)
}

// runCodexDirect builds the Codex transport request through the backend-local
// `adaptercodex.BuildRequest` entrypoint, then dispatches it through the
// websocket-or-HTTP selector that the Codex package owns. The root facade keeps
// only auth/account plumbing.
func (s *Server) runCodexDirect(
	ctx context.Context,
	req ChatRequest,
	model ResolvedModel,
	effort string,
	reqID string,
	emit func(adapteropenai.StreamChunk) error,
) (codexRunResult, error) {
	token, err := s.readCodexAccessToken()
	if err != nil {
		return adaptercodex.NewRunResult("stop"), err
	}
	return adaptercodex.RunDirect(ctx, adaptercodex.DirectConfig{
		HTTPClient:       s.httpClient,
		BaseURL:          s.codexBaseURL(),
		WebsocketEnabled: s.codexWebsocketEnabled(),
		WebsocketURL:     s.codexWebsocketURL(),
		Token:            token,
		AccountID:        s.readCodexAccountID(),
		RequestID:        reqID,
		Continuation:     s.codexContinue,
		Log:              s.log,
	}, req, model, effort, emit)
}
