package codex

import "net/http"

const openAIBetaHeader = "OpenAI-Beta"
const responsesWebsocketsV2BetaHeaderValue = "responses_websockets=2026-02-06"

func BuildResponsesWebsocketHeaders(requestID, token string) http.Header {
    header := http.Header{}
    if token != "" {
        header.Set("Authorization", "Bearer "+token)
    }
    if requestID != "" {
        header.Set("x-client-request-id", requestID)
    }
    header.Set(openAIBetaHeader, responsesWebsocketsV2BetaHeaderValue)
    return header
}
