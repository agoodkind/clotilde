package codex

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

type DirectConfig struct {
	HTTPClient       *http.Client
	BaseURL          string
	WebsocketEnabled bool
	WebsocketURL     string
	Token            string
	AccountID        string
	RequestID        string
	// SessionCache enables persistent ws session reuse with chained
	// previous_response_id and delta input. Constructed once per
	// Provider. Required: RunDirect refuses to run without it.
	SessionCache *WebsocketSessionCache
	Log          *slog.Logger
	BodyLog      BodyLogConfig
}

func RunDirect(
	ctx context.Context,
	cfg DirectConfig,
	req adapteropenai.ChatRequest,
	model adaptermodel.ResolvedModel,
	effort string,
	emit func(adapteropenai.StreamChunk) error,
) (RunResult, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	if !cfg.WebsocketEnabled {
		return NewRunResult("stop"), errCodexWebsocketDisabled
	}
	transportPayload := BuildRequest(req, model, effort)
	conversationID := strings.TrimSpace(transportPayload.PromptCache)
	if conversationID != "" {
		installationID, _ := LoadInstallationID()
		turnMeta := NewTurnMetadata(conversationID, "")
		turnMetaJSON, _ := turnMeta.MarshalCompact()
		transportPayload.ClientMetadata = ClientMetadataWithTurn(installationID, CodexWindowID(conversationID), turnMetaJSON)
	}
	wsReq := ResponseCreateRequestFromHTTP(transportPayload)
	wsCfg := WebsocketTransportConfig{
		URL:            cfg.WebsocketURL,
		Token:          cfg.Token,
		AccountID:      cfg.AccountID,
		RequestID:      cfg.RequestID,
		Alias:          model.Alias,
		ConversationID: conversationID,
		TurnState:      NewTurnState(),
		BodyLog:        cfg.BodyLog,
		SessionCache:   cfg.SessionCache,
		Log:            cfg.Log,
	}
	return RunWebsocketTransport(ctx, wsCfg, wsReq, emit)
}

var errCodexWebsocketDisabled = errors.New("codex websocket transport is disabled but no HTTPS fallback exists")
