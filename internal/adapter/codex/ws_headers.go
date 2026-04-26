package codex

import (
	"net/http"
	"strings"
)

const openAIBetaHeader = "OpenAI-Beta"
const responsesWebsocketsV2BetaHeaderValue = "responses_websockets=2026-02-06"

type ResponsesWebsocketHeaderConfig struct {
	RequestID            string
	ConversationID       string
	Token                string
	InstallationID       string
	WindowID             string
	BetaFeatures         string
	TurnState            *TurnState
	TurnMetadata         string
	IncludeTimingMetrics bool
}

func BuildResponsesWebsocketHeaders(cfg ResponsesWebsocketHeaderConfig) http.Header {
	header := http.Header{}
	if token := strings.TrimSpace(cfg.Token); token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	conversationID := strings.TrimSpace(cfg.ConversationID)
	clientRequestID := conversationID
	if clientRequestID == "" {
		clientRequestID = strings.TrimSpace(cfg.RequestID)
	}
	if clientRequestID != "" {
		header.Set("x-client-request-id", clientRequestID)
	}
	if conversationID != "" {
		header.Set("session_id", conversationID)
	}
	if installationID := strings.TrimSpace(cfg.InstallationID); installationID != "" {
		header.Set(CodexInstallationIDHeader, installationID)
	}
	windowID := strings.TrimSpace(cfg.WindowID)
	if windowID == "" {
		windowID = CodexWindowID(conversationID)
	}
	if windowID != "" {
		header.Set(CodexWindowIDHeader, windowID)
	}
	if betaFeatures := strings.TrimSpace(cfg.BetaFeatures); betaFeatures != "" {
		header.Set(CodexBetaFeaturesHeader, betaFeatures)
	}
	if turnState := cfg.TurnState.Value(); turnState != "" {
		header.Set(CodexTurnStateHeader, turnState)
	}
	if turnMetadata := strings.TrimSpace(cfg.TurnMetadata); turnMetadata != "" {
		header.Set(CodexTurnMetadataHeader, turnMetadata)
	}
	if cfg.IncludeTimingMetrics {
		header.Set(CodexTimingMetricsHeader, "true")
	}
	header.Set(openAIBetaHeader, responsesWebsocketsV2BetaHeaderValue)
	return header
}
