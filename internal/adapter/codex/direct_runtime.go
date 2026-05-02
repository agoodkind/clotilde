package codex

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
	"goodkind.io/clyde/internal/correlation"
)

type DirectConfig struct {
	HTTPClient       *http.Client
	BaseURL          string
	WebsocketEnabled bool
	WebsocketURL     string
	Token            string
	AccountID        string
	RequestID        string
	CursorRequestID  string
	Correlation      correlation.Context
	// WorkspacePath is the absolute path to the Cursor-active
	// workspace, used to populate the `workspaces` block in
	// `x-codex-turn-metadata`. Empty when Cursor did not supply a
	// workspace.
	WorkspacePath string
	// WorkspaceProbe runs the small git probe (origin / HEAD /
	// has_changes). Optional. When nil, the workspace block is
	// emitted with the path only and no git fields.
	WorkspaceProbe *WorkspaceProbe
	// SessionCache enables persistent ws session reuse with chained
	// previous_response_id and delta input. Constructed once per
	// Provider. Required: RunDirect refuses to run without it.
	SessionCache *WebsocketSessionCache
	Log          *slog.Logger
	BodyLog      BodyLogConfig
	FileLog      FileLogRotationConfig
}

func RunDirect(
	ctx context.Context,
	cfg DirectConfig,
	req adapteropenai.ChatRequest,
	model adaptermodel.ResolvedModel,
	effort string,
	emit func(adapterrender.Event) error,
) (RunResult, error) {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = http.DefaultClient
	}
	ConfigureCodexFileLogger(cfg.FileLog)
	if !cfg.WebsocketEnabled {
		return NewRunResult("stop"), errCodexWebsocketDisabled
	}
	transportPayload := BuildRequest(req, model, effort)
	conversationID := strings.TrimSpace(transportPayload.PromptCache)
	if conversationID != "" {
		installationID, _ := LoadInstallationID()
		turnMeta := NewTurnMetadata(conversationID, "")
		if ws := strings.TrimSpace(cfg.WorkspacePath); ws != "" {
			entry := TurnMetadataWorkspace{}
			if cfg.WorkspaceProbe != nil {
				entry = cfg.WorkspaceProbe.Probe(ws)
			}
			turnMeta = turnMeta.WithWorkspace(ws, entry)
		}
		turnMetaJSON, _ := turnMeta.MarshalCompact()
		transportPayload.ClientMetadata = ClientMetadataWithTurn(installationID, CodexWindowID(conversationID), turnMetaJSON)
	}
	wsReq := ResponseCreateRequestFromHTTP(transportPayload)
	wsCfg := WebsocketTransportConfig{
		URL:             cfg.WebsocketURL,
		Token:           cfg.Token,
		AccountID:       cfg.AccountID,
		RequestID:       cfg.RequestID,
		CursorRequestID: cfg.CursorRequestID,
		Correlation:     cfg.Correlation,
		Alias:           model.Alias,
		ConversationID:  conversationID,
		TurnState:       NewTurnState(),
		TurnMetadata:    transportPayload.ClientMetadata[CodexTurnMetadataHeader],
		BodyLog:         cfg.BodyLog,
		SessionCache:    cfg.SessionCache,
		Log:             cfg.Log,
	}
	return RunWebsocketTransportEvents(ctx, wsCfg, wsReq, emit)
}

var errCodexWebsocketDisabled = errors.New("codex websocket transport is disabled but no HTTPS fallback exists")
