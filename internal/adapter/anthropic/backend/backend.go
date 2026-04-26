package anthropicbackend

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"goodkind.io/clyde/internal/adapter/anthropic"
	adaptermodel "goodkind.io/clyde/internal/adapter/model"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

type FallbackConfig struct {
	Enabled            bool
	Trigger            string
	ForwardToShunt     bool
	ForwardToShuntName string
	FailureEscalation  string
}

type Dispatcher interface {
	AnthropicConfigured() bool
	AcquirePrimary(any) error
	ReleasePrimary()
	BuildAnthropicWire(adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, any, string) (anthropic.Request, error)
	StreamOAuth(http.ResponseWriter, *http.Request, anthropic.Request, adaptermodel.ResolvedModel, string, time.Time, bool, bool, string) error
	CollectOAuth(http.ResponseWriter, any, anthropic.Request, adaptermodel.ResolvedModel, string, time.Time, any, bool, string) error
	ParseResponseFormat(any) any
	RequestContextTrackerKey(adapteropenai.ChatRequest, string) string
	WriteError(http.ResponseWriter, int, string, string)
	HandleOAuth(http.ResponseWriter, *http.Request, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, string, bool) error
	HandleFallback(http.ResponseWriter, *http.Request, adapteropenai.ChatRequest, adaptermodel.ResolvedModel, string, bool) error
	ForwardShunt(http.ResponseWriter, *http.Request, adaptermodel.ResolvedModel, string, []byte)
	HasShunt(name string) bool
	SurfaceFallbackFailure(http.ResponseWriter, error, error, string)
	Log() *slog.Logger
}

func Handle(d Dispatcher, w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, escalate bool) error {
	if !d.AnthropicConfigured() {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("oauth_unconfigured: adapter built without anthropic client"),
			escalate,
			func(status int, code, msg string) error {
				d.WriteError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"oauth_unconfigured",
			"adapter built without anthropic client; set adapter.direct_oauth=true and restart",
		); err != nil {
			return err
		}
		return nil
	}
	if err := d.AcquirePrimary(r.Context()); err != nil {
		if err2 := adapterruntime.EscalateOrWrite(
			fmt.Errorf("rate_limited: %w", err),
			escalate,
			func(status int, code, msg string) error {
				d.WriteError(w, status, code, msg)
				return nil
			},
			http.StatusTooManyRequests,
			"rate_limited",
			fmt.Sprint(err),
		); err2 != nil {
			return err2
		}
		return nil
	}
	defer d.ReleasePrimary()

	jsonSpec := d.ParseResponseFormat(req.ResponseFormat)
	trackerKey := d.RequestContextTrackerKey(req, model.Alias)
	anthReq, err := d.BuildAnthropicWire(req, model, effort, jsonSpec, reqID)
	if err != nil {
		if err2 := adapterruntime.EscalateOrWrite(
			fmt.Errorf("oauth_translate: %w", err),
			escalate,
			func(status int, code, msg string) error {
				d.WriteError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"invalid_request",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}

	started := time.Now()
	if req.Stream {
		_ = req.StreamOptions
		return d.StreamOAuth(w, r, anthReq, model, reqID, started, escalate, true, trackerKey)
	}
	return d.CollectOAuth(w, r.Context(), anthReq, model, reqID, started, jsonSpec, escalate, trackerKey)
}

func Dispatch(d Dispatcher, cfg FallbackConfig, w http.ResponseWriter, r *http.Request, req adapteropenai.ChatRequest, model adaptermodel.ResolvedModel, effort, reqID string, body []byte) {
	escalate := cfg.Enabled &&
		(cfg.Trigger == adaptermodel.FallbackTriggerOnOAuthFailure || cfg.Trigger == adaptermodel.FallbackTriggerBoth)
	if !escalate {
		_ = d.HandleOAuth(w, r, req, model, effort, reqID, false)
		return
	}

	anthErr := d.HandleOAuth(w, r, req, model, effort, reqID, true)
	if anthErr == nil {
		return
	}

	classification := classifyEscalationCause(anthErr)
	d.Log().LogAttrs(r.Context(), slog.LevelWarn, "adapter.fallback.escalating",
		slog.String("request_id", reqID),
		slog.String("alias", model.Alias),
		slog.String("anthropic_err", anthErr.Error()),
		slog.String("anthropic_class", classification.class),
		slog.Int("anthropic_status", classification.status),
		slog.Bool("anthropic_retryable", classification.retryable),
		slog.Bool("forward_to_shunt", cfg.ForwardToShunt),
	)

	if cfg.ForwardToShunt {
		if !d.HasShunt(cfg.ForwardToShuntName) {
			d.Log().LogAttrs(r.Context(), slog.LevelError, "adapter.fallback.shunt_unconfigured",
				slog.String("request_id", reqID),
				slog.String("shunt", cfg.ForwardToShuntName),
			)
			d.SurfaceFallbackFailure(w, anthErr, fmt.Errorf("forward_to_shunt %q not configured (base_url empty)", cfg.ForwardToShuntName), cfg.FailureEscalation)
			return
		}
		d.ForwardShunt(w, r, model, cfg.ForwardToShuntName, body)
		return
	}

	fbErr := d.HandleFallback(w, r, req, model, reqID, true)
	if fbErr == nil {
		return
	}
	d.SurfaceFallbackFailure(w, anthErr, fbErr, cfg.FailureEscalation)
}

// escalationClassification names the Anthropic classifier outcome for
// the failure that triggered the fallback. The fields are flat
// strings/ints so they can land on a single slog event without forcing
// the dispatcher to reach into the typed Anthropic error.
type escalationClassification struct {
	class     string
	status    int
	retryable bool
}

func classifyEscalationCause(err error) escalationClassification {
	if err == nil {
		return escalationClassification{class: "untyped"}
	}
	ue, ok := anthropic.AsUpstreamError(err)
	if !ok {
		return escalationClassification{class: "untyped"}
	}
	return escalationClassification{
		class:     ue.Classification.Class.String(),
		status:    ue.Status,
		retryable: ue.Retryable(),
	}
}
