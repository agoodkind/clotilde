package codex

import (
	"net/http"
	"strings"
	"sync"
)

const (
	CodexInstallationIDHeader = "x-codex-installation-id"
	CodexTurnStateHeader      = "x-codex-turn-state"
	CodexTurnMetadataHeader   = "x-codex-turn-metadata"
	CodexWindowIDHeader       = "x-codex-window-id"
	CodexTimingMetricsHeader  = "x-responsesapi-include-timing-metrics"
	CodexBetaFeaturesHeader   = "x-codex-beta-features"
)

type TurnState struct {
	mu    sync.RWMutex
	value string
}

func NewTurnState() *TurnState {
	return &TurnState{}
}

func (s *TurnState) Value() string {
	if s == nil {
		return ""
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.value
}

func (s *TurnState) CaptureFromHeaders(header http.Header) bool {
	if s == nil {
		return false
	}
	value := strings.TrimSpace(header.Get(CodexTurnStateHeader))
	if value == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value != "" {
		return false
	}
	s.value = value
	return true
}

func CodexWindowID(conversationID string) string {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return ""
	}
	return conversationID + ":0"
}
