package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
)

type preflightError struct {
	code int
	body ErrorBody
}

func toolChoiceRequestsTools(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return str != "" && str != "none"
	}
	return true
}

func errAudioUnsupported() *preflightError {
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: "audio content parts are not supported by this adapter",
			Type:    "invalid_request_error",
			Code:    "audio_unsupported",
		},
	}
}

func errVisionAnthropicUnsupported(modelAlias string) *preflightError {
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: fmt.Sprintf("model %q does not support vision input", modelAlias),
			Type:    "invalid_request_error",
			Code:    "unsupported_content",
		},
	}
}

func errVisionFallbackUnsupported() *preflightError {
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: "vision input is not supported on the fallback backend",
			Type:    "invalid_request_error",
			Code:    "fallback_no_vision",
		},
	}
}

func errToolNameEmpty(kind string, index int) *preflightError {
	var msg string
	if kind == "tools" {
		msg = fmt.Sprintf("tools[%d].function.name is required and must be non-empty", index)
	} else {
		msg = fmt.Sprintf("functions[%d].name is required and must be non-empty", index)
	}
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: msg,
			Type:    "invalid_request_error",
			Code:    "invalid_tool_name",
		},
	}
}

func errToolsNotEnabledForAlias() *preflightError {
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: "tools are not enabled for this model alias",
			Type:    "invalid_request_error",
			Code:    "unsupported_content",
		},
	}
}

func errLogprobsUnsupported() *preflightError {
	return &preflightError{
		code: http.StatusBadRequest,
		body: ErrorBody{
			Message: "logprobs are not supported for this backend",
			Type:    "invalid_request_error",
			Code:    "unsupported_param",
		},
	}
}

func (s *Server) validateAudio(ctx context.Context, req *ChatRequest, reqID string) *preflightError {
	for msgIdx := range req.Messages {
		parts, _ := NormalizeContent(req.Messages[msgIdx].Content)
		for _, p := range parts {
			if p.Type != "input_audio" {
				continue
			}
			s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.audio_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("message_index", msgIdx),
			)
			return errAudioUnsupported()
		}
	}
	return nil
}

func requestHasImageContent(req *ChatRequest) bool {
	for _, msg := range req.Messages {
		parts, _ := NormalizeContent(msg.Content)
		for _, p := range parts {
			if p.Type == "image_url" {
				return true
			}
		}
	}
	return false
}

func (s *Server) validateVision(ctx context.Context, req *ChatRequest, model ResolvedModel, reqID string) *preflightError {
	if model.Backend == BackendShunt {
		return nil
	}
	if !requestHasImageContent(req) {
		return nil
	}
	if model.Backend == BackendAnthropic && !model.SupportsVision {
		s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.vision_rejected",
			slog.String("request_id", reqID),
			slog.String("model", req.Model),
		)
		return errVisionAnthropicUnsupported(req.Model)
	}
	if model.Backend == BackendFallback {
		s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.vision_rejected",
			slog.String("request_id", reqID),
			slog.String("model", req.Model),
		)
		return errVisionFallbackUnsupported()
	}
	return nil
}

func (s *Server) validateTools(ctx context.Context, req *ChatRequest, reqID string) *preflightError {
	for tIdx, t := range req.Tools {
		if t.Function.Name == "" {
			s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.tools_invalid_name",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("tool_index", tIdx),
				slog.String("reason", "empty function.name"),
			)
			return errToolNameEmpty("tools", tIdx)
		}
	}
	for fIdx, f := range req.Functions {
		if f.Name == "" {
			s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.tools_invalid_name",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.Int("function_index", fIdx),
				slog.String("reason", "empty functions[].name"),
			)
			return errToolNameEmpty("functions", fIdx)
		}
	}
	return nil
}

func (s *Server) validateToolChoice(ctx context.Context, req *ChatRequest, model ResolvedModel, reqID string) *preflightError {
	if model.Backend != BackendAnthropic {
		return nil
	}
	wantsTools := len(req.Tools) > 0 || len(req.Functions) > 0 || toolChoiceRequestsTools(req.ToolChoice)
	if wantsTools && !model.SupportsTools {
		s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.tools_rejected",
			slog.String("request_id", reqID),
			slog.String("model", req.Model),
		)
		return errToolsNotEnabledForAlias()
	}
	return nil
}

func (s *Server) validateLogprobs(ctx context.Context, req *ChatRequest, model ResolvedModel, reqID string) *preflightError {
	wantsLogprobs := (req.Logprobs != nil && *req.Logprobs) || req.TopLogprobs != nil
	if !wantsLogprobs {
		return nil
	}
	switch model.Backend {
	case BackendAnthropic:
		switch s.logprobs.Anthropic {
		case "reject":
			s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.logprobs_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.String("backend", "anthropic"),
			)
			return errLogprobsUnsupported()
		case "drop":
			req.Logprobs = nil
			req.TopLogprobs = nil
		}
	case BackendFallback:
		switch s.logprobs.Fallback {
		case "reject":
			s.log.LogAttrs(ctx, slog.LevelWarn, "adapter.preflight.logprobs_rejected",
				slog.String("request_id", reqID),
				slog.String("model", req.Model),
				slog.String("backend", "fallback"),
			)
			return errLogprobsUnsupported()
		case "drop":
			req.Logprobs = nil
			req.TopLogprobs = nil
		}
	}
	return nil
}

// preflightChat enforces adapter capability gates after alias resolution.
// It may mutate req when logprobs policy is "drop".
func (s *Server) preflightChat(ctx context.Context, req *ChatRequest, model ResolvedModel, reqID string) *preflightError {
	if err := s.validateAudio(ctx, req, reqID); err != nil {
		return err
	}
	if err := s.validateVision(ctx, req, model, reqID); err != nil {
		return err
	}
	if err := s.validateTools(ctx, req, reqID); err != nil {
		return err
	}
	if err := s.validateToolChoice(ctx, req, model, reqID); err != nil {
		return err
	}
	return s.validateLogprobs(ctx, req, model, reqID)
}
