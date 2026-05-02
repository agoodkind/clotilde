package adapter

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"goodkind.io/clyde/internal/slogger"
)

type adapterRouteFamily string

const (
	adapterRouteOpenAI    adapterRouteFamily = "openai"
	adapterRouteAnthropic adapterRouteFamily = "anthropic"
	adapterRouteHealth    adapterRouteFamily = "health"
)

type adapterErrorClass string

const (
	adapterErrorAuthFailed            adapterErrorClass = "auth_failed"
	adapterErrorMethodNotAllowed      adapterErrorClass = "method_not_allowed"
	adapterErrorInvalidJSON           adapterErrorClass = "invalid_json"
	adapterErrorInvalidRequest        adapterErrorClass = "invalid_request"
	adapterErrorModelNotFound         adapterErrorClass = "model_not_found"
	adapterErrorModelNotSupported     adapterErrorClass = "model_not_supported"
	adapterErrorUnsupportedBackend    adapterErrorClass = "unsupported_backend"
	adapterErrorUnsupportedContent    adapterErrorClass = "unsupported_content"
	adapterErrorContextLengthExceeded adapterErrorClass = "context_length_exceeded"
	adapterErrorRateLimited           adapterErrorClass = "rate_limited"
	adapterErrorUpstreamAuthFailed    adapterErrorClass = "upstream_auth_failed"
	adapterErrorUpstreamUnavailable   adapterErrorClass = "upstream_unavailable"
	adapterErrorUpstreamFailed        adapterErrorClass = "upstream_failed"
	adapterErrorTimeout               adapterErrorClass = "timeout"
	adapterErrorCanceled              adapterErrorClass = "canceled"
	adapterErrorInternal              adapterErrorClass = "internal"
)

type adapterError struct {
	Class          adapterErrorClass
	HTTPStatus     int
	Message        string
	OpenAIType     string
	OpenAICode     string
	OpenAIParam    string
	AnthropicType  string
	Provider       string
	Backend        string
	ModelAlias     string
	ResolvedModel  string
	UpstreamStatus int
	Cause          error
	SafeForClient  bool
}

func (e *adapterError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return string(e.Class)
}

func (e *adapterError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newAdapterError(class adapterErrorClass, message string) *adapterError {
	e := &adapterError{
		Class:         class,
		Message:       strings.TrimSpace(message),
		SafeForClient: true,
	}
	e.applyDefaults()
	return e
}

func adapterErrInvalidJSON(message string, cause error) *adapterError {
	e := newAdapterError(adapterErrorInvalidJSON, message)
	e.Cause = cause
	return e
}

func adapterErrInvalidRequest(message string, cause error) *adapterError {
	e := newAdapterError(adapterErrorInvalidRequest, message)
	e.Cause = cause
	return e
}

func adapterErrModelNotFound(message string) *adapterError {
	return newAdapterError(adapterErrorModelNotFound, message)
}

func adapterErrInternal(message string, cause error) *adapterError {
	e := newAdapterError(adapterErrorInternal, message)
	e.Cause = cause
	e.SafeForClient = false
	return e
}

func adapterErrUpstreamFailed(provider, message string, cause error) *adapterError {
	e := newAdapterError(adapterErrorUpstreamFailed, message)
	e.Provider = strings.TrimSpace(provider)
	e.Cause = cause
	return e
}

func adapterErrFromOpenAI(status int, body ErrorBody) *adapterError {
	class := adapterErrorInvalidRequest
	switch strings.TrimSpace(body.Code) {
	case "context_length_exceeded":
		class = adapterErrorContextLengthExceeded
	case "model_not_supported":
		class = adapterErrorModelNotSupported
	case "model_not_found":
		class = adapterErrorModelNotFound
	case "unsupported_backend":
		class = adapterErrorUnsupportedBackend
	case "unsupported_content", "audio_unsupported":
		class = adapterErrorUnsupportedContent
	case "rate_limit_exceeded":
		class = adapterErrorRateLimited
	case "upstream_unavailable":
		class = adapterErrorUpstreamUnavailable
	case "upstream_failed":
		class = adapterErrorUpstreamFailed
	case "internal_error":
		class = adapterErrorInternal
	}
	e := newAdapterError(class, body.Message)
	e.OpenAIType = body.Type
	e.OpenAICode = body.Code
	e.OpenAIParam = body.Param
	e.applyDefaults()
	if status > 0 {
		e.HTTPStatus = status
	}
	return e
}

func (e *adapterError) applyDefaults() {
	explicitStatus := e.HTTPStatus
	explicitOpenAIType := e.OpenAIType
	explicitOpenAICode := e.OpenAICode
	explicitOpenAIParam := e.OpenAIParam
	explicitAnthropicType := e.AnthropicType
	if e.HTTPStatus == 0 {
		e.HTTPStatus = http.StatusInternalServerError
	}
	switch e.Class {
	case adapterErrorAuthFailed:
		e.HTTPStatus = http.StatusUnauthorized
		e.OpenAIType = "authentication_error"
		e.OpenAICode = "unauthorized"
		e.AnthropicType = "authentication_error"
	case adapterErrorMethodNotAllowed:
		e.HTTPStatus = http.StatusMethodNotAllowed
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "method_not_allowed"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorInvalidJSON:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "invalid_json"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorInvalidRequest:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "invalid_request"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorModelNotFound:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "model_not_found"
		e.OpenAIParam = "model"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorModelNotSupported:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "model_not_supported"
		e.OpenAIParam = "model"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorUnsupportedBackend:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "unsupported_backend"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorUnsupportedContent:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "unsupported_content"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorContextLengthExceeded:
		e.HTTPStatus = http.StatusBadRequest
		e.OpenAIType = "invalid_request_error"
		e.OpenAICode = "context_length_exceeded"
		e.OpenAIParam = "messages"
		e.AnthropicType = "invalid_request_error"
	case adapterErrorRateLimited:
		e.HTTPStatus = http.StatusTooManyRequests
		e.OpenAIType = "rate_limit_error"
		e.OpenAICode = "rate_limit_exceeded"
		e.AnthropicType = "rate_limit_error"
	case adapterErrorUpstreamAuthFailed:
		e.HTTPStatus = http.StatusUnauthorized
		e.OpenAIType = "authentication_error"
		e.OpenAICode = "upstream_auth_failed"
		e.AnthropicType = "authentication_error"
	case adapterErrorUpstreamUnavailable:
		e.HTTPStatus = http.StatusBadGateway
		e.OpenAIType = "server_error"
		e.OpenAICode = "upstream_unavailable"
		e.AnthropicType = "api_error"
	case adapterErrorUpstreamFailed:
		e.HTTPStatus = http.StatusBadGateway
		e.OpenAIType = "server_error"
		e.OpenAICode = "upstream_failed"
		e.AnthropicType = "api_error"
	case adapterErrorTimeout:
		e.HTTPStatus = http.StatusGatewayTimeout
		e.OpenAIType = "server_error"
		e.OpenAICode = "timeout"
		e.AnthropicType = "api_error"
	case adapterErrorCanceled:
		e.HTTPStatus = 499
		e.OpenAIType = "server_error"
		e.OpenAICode = "canceled"
		e.AnthropicType = "api_error"
	case adapterErrorInternal:
		e.HTTPStatus = http.StatusInternalServerError
		e.OpenAIType = "internal_error"
		e.OpenAICode = "internal_error"
		e.AnthropicType = "api_error"
	}
	if e.Message == "" {
		e.Message = string(e.Class)
	}
	if explicitStatus > 0 {
		e.HTTPStatus = explicitStatus
	}
	if explicitOpenAIType != "" {
		e.OpenAIType = explicitOpenAIType
	}
	if explicitOpenAICode != "" {
		e.OpenAICode = explicitOpenAICode
	}
	if explicitOpenAIParam != "" {
		e.OpenAIParam = explicitOpenAIParam
	}
	if explicitAnthropicType != "" {
		e.AnthropicType = explicitAnthropicType
	}
}

func adapterRouteFamilyForPath(path string) adapterRouteFamily {
	switch {
	case strings.HasPrefix(path, "/v1/messages"):
		return adapterRouteAnthropic
	case path == "/healthz":
		return adapterRouteHealth
	default:
		return adapterRouteOpenAI
	}
}

func adapterErrorFrom(err error) *adapterError {
	if err == nil {
		return nil
	}
	var aerr *adapterError
	if errors.As(err, &aerr) {
		aerr.applyDefaults()
		return aerr
	}
	return adapterErrUpstreamFailed("", err.Error(), err)
}

func (s *Server) respondAdapterError(w http.ResponseWriter, r *http.Request, err error) {
	aerr := adapterErrorFrom(err)
	if aerr == nil {
		aerr = adapterErrInternal("adapter internal error", nil)
	}
	corr := correlationForRequest(r)
	message := aerr.Message
	if !aerr.SafeForClient {
		message = "adapter internal error"
		if corr.RequestID != "" {
			message += "; see Clyde logs with request_id " + corr.RequestID
		}
	}
	family := adapterRouteFamilyForPath(r.URL.Path)
	attrs := []slog.Attr{
		slog.String("method", r.Method),
		slog.String("path", r.URL.Path),
		slog.String("route_family", string(family)),
		slog.Int("status", aerr.HTTPStatus),
		slog.String("error_class", string(aerr.Class)),
		slog.String("openai_type", aerr.OpenAIType),
		slog.String("openai_code", aerr.OpenAICode),
		slog.String("anthropic_type", aerr.AnthropicType),
		slog.String("model", aerr.ModelAlias),
		slog.String("backend", aerr.Backend),
		slog.String("provider", aerr.Provider),
		slog.Int("upstream_status", aerr.UpstreamStatus),
		slog.Bool("response_started", false),
		slog.Bool("safe_for_client", aerr.SafeForClient),
		slog.String("err", aerr.Error()),
	}
	attrs = append(attrs, corr.Attrs()...)
	slogger.WithConcern(s.log, slogger.ConcernAdapterHTTPErrors).LogAttrs(r.Context(), slog.LevelWarn, "adapter.error.responded", attrs...)
	switch family {
	case adapterRouteAnthropic:
		writeAnthropicErrorBody(w, aerr.HTTPStatus, aerr.AnthropicType, message)
	default:
		writeOpenAIErrorBody(w, aerr.HTTPStatus, ErrorBody{
			Message: message,
			Type:    aerr.OpenAIType,
			Code:    aerr.OpenAICode,
			Param:   aerr.OpenAIParam,
		})
	}
}

func writeOpenAIErrorBody(w http.ResponseWriter, code int, body ErrorBody) {
	if body.Code == "" {
		body.Code = body.Type
	}
	writeJSON(w, code, ErrorResponse{Error: body})
}

func (e *adapterError) openAIErrorBody() ErrorBody {
	if e == nil {
		return ErrorBody{
			Message: "adapter internal error",
			Type:    "internal_error",
			Code:    "internal_error",
		}
	}
	e.applyDefaults()
	return ErrorBody{
		Message: e.Message,
		Type:    e.OpenAIType,
		Code:    e.OpenAICode,
		Param:   e.OpenAIParam,
	}
}
