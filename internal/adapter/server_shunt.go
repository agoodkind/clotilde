package adapter

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
	"goodkind.io/clyde/internal/correlation"
)

const structuredOutputShuntParseFailedEvent = "shunt structured-output parse failed; retrying"

func (s *Server) forwardShunt(w http.ResponseWriter, r *http.Request, model ResolvedModel, body []byte) {
	started := adapterClock.Now()
	reqID := newRequestID()
	corr := correlation.FromContext(r.Context()).Child().WithRequestID(reqID)
	if corr.TraceID == "" {
		corr = correlation.FromHTTPHeader(r.Header, reqID)
	}
	corr.SetHTTPHeaders(w.Header())
	ctx := correlation.WithContext(r.Context(), corr)
	r = r.WithContext(ctx)
	streamRequested := false
	baseURL := strings.TrimSpace(model.OpenAICompatPassthrough.BaseURL)
	apiKey := model.OpenAICompatPassthrough.APIKey
	apiKeyEnv := model.OpenAICompatPassthrough.APIKeyEnv
	modelOverride := model.OpenAICompatPassthrough.Model
	upstreamLabel := "openai_compat_passthrough"
	if baseURL == "" {
		shunt, ok := s.registry.Shunt(model.Shunt)
		if !ok || shunt.BaseURL == "" {
			err := newAdapterError(adapterErrorUpstreamUnavailable,
				"alias routes to shunt "+model.Shunt+" but no base URL is configured")
			err.Provider = providerName(model, "")
			err.Backend = model.Backend
			err.ModelAlias = model.Alias
			s.respondAdapterError(w, r, err)
			return
		}
		baseURL = shunt.BaseURL
		apiKey = shunt.APIKey
		apiKeyEnv = shunt.APIKeyEnv
		modelOverride = shunt.Model
		upstreamLabel = "shunt:" + model.Shunt
	}
	if apiKey == "" && apiKeyEnv != "" {
		apiKey = os.Getenv(apiKeyEnv)
	}

	// Mutate the request body if we need to. Two reasons:
	//   1. shunt.Model overrides the alias the caller sent.
	//   2. response_format json_schema does not work on most local
	//      backends (LM Studio, Ollama, etc.) so we strip it and
	//      prepend a system message that tells the model to emit
	//      raw JSON. The clyde adapter then post-validates and
	//      retries once if the reply does not parse.
	var rawReq map[string]any
	jsonSpec := JSONResponseSpec{}
	if err := json.Unmarshal(body, &rawReq); err == nil {
		if v, ok := rawReq["stream"].(bool); ok {
			streamRequested = v
		}
		if modelOverride != "" {
			rawReq["model"] = modelOverride
		}
		if rf, ok := rawReq["response_format"]; ok {
			rfBytes, _ := json.Marshal(rf)
			jsonSpec = ParseResponseFormat(rfBytes)
		}
		if jsonSpec.Mode != "" {
			injectJSONSystemMessage(rawReq, jsonSpec.SystemPrompt(false))
			delete(rawReq, "response_format")
		}
		body, _ = json.Marshal(rawReq)
	}
	s.emitRequestStarted(ctx, model, "", reqID, model.Alias, streamRequested)

	respBody, status, hdr, err := shuntCall(ctx, baseURL, apiKey, body)
	if err != nil {
		adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
			Stage:      adapterruntime.RequestStageFailed,
			Provider:   providerName(model, ""),
			Backend:    model.Backend,
			RequestID:  reqID,
			Alias:      model.Alias,
			ModelID:    model.Alias,
			Stream:     streamRequested,
			DurationMs: time.Since(started).Milliseconds(),
			Err:        err.Error(),
		})
		aerr := newAdapterError(adapterErrorUpstreamUnavailable, err.Error())
		aerr.Provider = providerName(model, "")
		aerr.Backend = model.Backend
		aerr.ModelAlias = model.Alias
		aerr.Cause = err
		s.respondAdapterError(w, r, aerr)
		return
	}
	contentType := strings.ToLower(strings.TrimSpace(hdr.Get("Content-Type")))
	if streamRequested || strings.Contains(contentType, "text/event-stream") {
		s.emitRequestStreamOpened(ctx, model, "", reqID, model.Alias, true)
	}

	if jsonSpec.Mode != "" && status == http.StatusOK {
		coerced, ok := coerceShuntJSON(respBody)
		if !ok {
			attrs := []slog.Attr{
				slog.String("model", model.Alias),
				slog.String("upstream", upstreamLabel),
				slog.Int("first_attempt_bytes", len(respBody)),
			}
			attrs = append(attrs, corr.Attrs()...)
			s.log.LogAttrs(ctx, slog.LevelWarn, structuredOutputShuntParseFailedEvent, attrs...)
			injectJSONSystemMessage(rawReq, jsonSpec.SystemPrompt(true))
			body2, _ := json.Marshal(rawReq)
			rb2, st2, h2, err2 := shuntCall(r.Context(), baseURL, apiKey, body2)
			if err2 == nil && st2 == http.StatusOK {
				if c2, ok2 := coerceShuntJSON(rb2); ok2 {
					respBody, status, hdr = c2, st2, h2
				} else {
					respBody, status, hdr = rb2, st2, h2
				}
			}
		} else {
			respBody = coerced
		}
	}
	if status >= http.StatusBadRequest {
		respBody, hdr = normalizeShuntErrorResponse(status, respBody, hdr)
	}

	for k, v := range hdr {
		// Drop any upstream-set Content-Length; we may have rewritten
		// the body and a stale length triggers the http2 framework to
		// return zero bytes to the client.
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		w.Header()[k] = v
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(respBody)))
	w.WriteHeader(status)
	_, _ = w.Write(respBody)

	usage := shuntUsageFromBody(respBody)
	stage := adapterruntime.RequestStageCompleted
	terminalErr := ""
	if status >= 400 {
		stage = adapterruntime.RequestStageFailed
		terminalErr = "upstream returned status " + strconv.Itoa(status)
	}
	adapterruntime.LogTerminal(s.log, ctx, s.deps.RequestEvents, adapterruntime.RequestEvent{
		Stage:               stage,
		Provider:            providerName(model, ""),
		Backend:             model.Backend,
		RequestID:           reqID,
		Alias:               model.Alias,
		ModelID:             model.Alias,
		Stream:              streamRequested || strings.Contains(contentType, "text/event-stream"),
		TokensIn:            usage.PromptTokens,
		TokensOut:           usage.CompletionTokens,
		CacheReadTokens:     usage.CachedTokens(),
		CacheCreationTokens: 0,
		DurationMs:          time.Since(started).Milliseconds(),
		Err:                 terminalErr,
	})
}

// injectJSONSystemMessage prepends (or appends to existing system
// content) an instruction telling the model to emit raw JSON only.
func injectJSONSystemMessage(req map[string]any, instruction string) {
	if instruction == "" {
		return
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) > 0 {
		first, _ := msgs[0].(map[string]any)
		if first != nil {
			role, _ := first["role"].(string)
			if role == "system" || role == "developer" {
				if existing, ok := first["content"].(string); ok {
					first["content"] = instruction + "\n\n" + existing
				} else {
					first["content"] = instruction
				}
				msgs[0] = first
				req["messages"] = msgs
				return
			}
		}
	}
	sys := map[string]any{"role": "system", "content": instruction}
	req["messages"] = append([]any{sys}, msgs...)
}

// shuntCall posts body to the shunt's chat/completions endpoint and
// returns body+status+headers.
func shuntCall(ctx context.Context, baseURL, apiKey string, body []byte) ([]byte, int, http.Header, error) {
	target := strings.TrimRight(baseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, resp.Header, err
	}
	return rb, resp.StatusCode, resp.Header, nil
}

func normalizeShuntErrorResponse(status int, body []byte, hdr http.Header) ([]byte, http.Header) {
	if validOpenAIErrorEnvelope(body) {
		return body, hdr
	}
	envelope := ErrorResponse{Error: ErrorBody{
		Message: shuntUpstreamErrorMessage(status, body),
		Type:    "upstream_error",
		Code:    "upstream_failed",
	}}
	out, err := json.Marshal(envelope)
	if err != nil {
		return body, hdr
	}
	next := hdr.Clone()
	if next == nil {
		next = http.Header{}
	}
	next.Set("Content-Type", "application/json")
	return out, next
}

func validOpenAIErrorEnvelope(body []byte) bool {
	var envelope ErrorResponse
	if err := json.Unmarshal(body, &envelope); err != nil {
		return false
	}
	return strings.TrimSpace(envelope.Error.Message) != "" && strings.TrimSpace(envelope.Error.Type) != ""
}

func shuntUpstreamErrorMessage(status int, body []byte) string {
	text := strings.TrimSpace(strings.ToValidUTF8(string(body), ""))
	if text == "" {
		return "shunt upstream returned HTTP " + strconv.Itoa(status) + " with an empty error body"
	}
	const maxLen = 1000
	if len(text) > maxLen {
		text = text[:maxLen] + "..."
	}
	return "shunt upstream returned HTTP " + strconv.Itoa(status) + ": " + text
}

// coerceShuntJSON walks the OpenAI-shaped response, runs CoerceJSON
// on choices[].message.content, and returns the rewritten body if all
// choices now parse as JSON.
func coerceShuntJSON(body []byte) ([]byte, bool) {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		return body, false
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return body, false
	}
	allOK := true
	for i, c := range choices {
		choice, _ := c.(map[string]any)
		if choice == nil {
			allOK = false
			continue
		}
		msg, _ := choice["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].(string)
		if content == "" {
			continue
		}
		coerced := CoerceJSON(content)
		if !LooksLikeJSON(coerced) {
			allOK = false
			continue
		}
		msg["content"] = coerced
		choice["message"] = msg
		choices[i] = choice
	}
	resp["choices"] = choices
	out, _ := json.Marshal(resp)
	return out, allOK
}

func redactedHeaders(input http.Header) map[string]string {
	out := make(map[string]string, len(input))
	for key, values := range input {
		normalized := strings.ToLower(key)
		if redactedHeader(normalized) {
			out[normalized] = "[redacted]"
			continue
		}
		out[normalized] = strings.Join(values, ", ")
	}
	return out
}

func redactedHeader(name string) bool {
	switch name {
	case "authorization", "proxy-authorization", "cookie", "set-cookie", "x-clyde-token", "x-amz-security-token", "openai-api-key":
		return true
	case "":
		return false
	}
	if strings.HasPrefix(name, "x-cursor-") {
		return true
	}
	if strings.HasPrefix(name, "openai-") {
		return true
	}
	return strings.HasSuffix(name, "-api-key")
}

func shuntUsageFromBody(body []byte) Usage {
	var payload struct {
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Usage{}
	}
	usage := Usage{
		PromptTokens:     payload.Usage.PromptTokens,
		CompletionTokens: payload.Usage.CompletionTokens,
		TotalTokens:      payload.Usage.TotalTokens,
	}
	if payload.Usage.PromptDetails.CachedTokens > 0 {
		usage.PromptTokensDetails = &PromptTokensDetails{CachedTokens: payload.Usage.PromptDetails.CachedTokens}
	}
	return usage
}
