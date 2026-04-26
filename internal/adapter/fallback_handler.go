package adapter

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	anthropicbackend "goodkind.io/clyde/internal/adapter/anthropic/backend"
	"goodkind.io/clyde/internal/adapter/anthropic/fallback"
	adapterruntime "goodkind.io/clyde/internal/adapter/runtime"
)

// handleFallback fulfils a chat completion via the local `claude`
// CLI in `-p --output-format stream-json` mode. It is the third
// dispatch leg, gated by [adapter.fallback].
//
// When escalate is true (the on_oauth_failure / both triggers fired
// after an OAuth error), the function returns a non-nil error
// without writing the response on transport-level failures so the
// dispatcher can decide which error surfaces to the client per
// FailureEscalation. When escalate is false (explicit dispatch),
// errors are written to w directly.
func (s *Server) handleFallback(w http.ResponseWriter, r *http.Request, req ChatRequest, model ResolvedModel, reqID string, escalate bool) error {
	if s.fb == nil {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_unconfigured: adapter built without fallback client"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusInternalServerError,
			"fallback_unconfigured",
			"adapter built without fallback client; set adapter.fallback.enabled=true and restart",
		); err != nil {
			return err
		}
		return nil
	}
	if model.CLIAlias == "" {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_no_cli_alias: family %q has no CLI alias bound", model.FamilySlug),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"fallback_no_cli_alias",
			"alias resolves to a family with no [adapter.fallback.cli_aliases] entry; cannot dispatch via claude -p",
		); err != nil {
			return err
		}
		return nil
	}
	if req.Stream && !s.cfg.Fallback.StreamPassthrough {
		if err := adapterruntime.EscalateOrWrite(
			fmt.Errorf("fallback_stream_disabled: stream_passthrough=false"),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusBadRequest,
			"fallback_stream_disabled",
			"this adapter is configured with stream_passthrough=false; pass stream=false",
		); err != nil {
			return err
		}
		return nil
	}

	if err := s.acquireFallback(r.Context()); err != nil {
		if err2 := adapterruntime.EscalateOrWrite(
			fmt.Errorf("rate_limited: %w", err),
			escalate,
			func(status int, code, msg string) error {
				writeError(w, status, code, msg)
				return nil
			},
			http.StatusTooManyRequests,
			"rate_limited",
			err.Error(),
		); err2 != nil {
			return err2
		}
		return nil
	}
	defer s.releaseFallback()

	if s.cfg.Fallback.DropUnsupported {
		if req.ReasoningEffort != "" {
			s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.fallback.dropped_field",
				slog.String("request_id", reqID),
				slog.String("field", "reasoning_effort"),
				slog.String("value", req.ReasoningEffort),
				slog.String("reason", "claude -p has no effort flag; planned via settings.json injection"),
			)
		}
		if model.Thinking != "" && model.Thinking != ThinkingDefault {
			s.log.LogAttrs(r.Context(), slog.LevelDebug, "adapter.fallback.dropped_field",
				slog.String("request_id", reqID),
				slog.String("field", "thinking"),
				slog.String("value", model.Thinking),
				slog.String("reason", "claude -p has no thinking flag; planned via settings.json injection"),
			)
		}
	}

	fbReq := fallback.BuildRequest(fallback.RequestBuildInput{
		Model:      model.CLIAlias,
		ModelAlias: model.Alias,
		Messages:   req.Messages,
		Tools:      req.Tools,
		Functions:  req.Functions,
		ToolChoice: req.ToolChoice,
		RequestID:  reqID,
	})
	system := fbReq.System
	jsonSpec := ParseResponseFormat(req.ResponseFormat)
	if instr := jsonSpec.SystemPrompt(false); instr != "" {
		if system == "" {
			system = instr
		} else {
			system = system + "\n\n" + instr
		}
		fbReq.System = system
	}

	if s.cfg.Fallback.TranscriptSynthesisEnabled {
		workspaceDir := s.cfg.Fallback.ResolveTranscriptWorkspaceDir(model.Alias)
		resume := fallback.PrepareTranscriptResume(&fbReq, fallback.TranscriptResumeConfig{
			WorkspaceDir: workspaceDir,
		})
		if resume.Attempted {
			if resume.Err != nil {
				s.log.LogAttrs(r.Context(), slog.LevelWarn, "fallback.transcript.write_failed",
					slog.String("request_id", reqID),
					slog.String("session_id", resume.SessionID),
					slog.String("workspace_dir", resume.WorkspaceDir),
					slog.Any("err", resume.Err),
				)
			} else if resume.Resumed {
				s.log.LogAttrs(r.Context(), slog.LevelDebug, "fallback.transcript.resumed",
					slog.String("request_id", reqID),
					slog.String("session_id", resume.SessionID),
					slog.String("path", resume.Path),
					slog.Int("prior_turns", resume.PriorTurns),
				)
			}
		}
	}

	started := time.Now()
	if req.Stream {
		// Always emit the final usage chunk; see oauth_handler.go for rationale.
		_ = req.StreamOptions
		return anthropicbackend.StreamFallbackResponse(s, w, r, fbReq, model, reqID, started, escalate, true)
	}
	return anthropicbackend.CollectFallbackResponse(s, w, r.Context(), fbReq, model, reqID, started, jsonSpec, escalate)
}

// acquireFallback waits on the fallback's own concurrency semaphore.
func (s *Server) acquireFallback(ctx context.Context) error {
	select {
	case s.fbSem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return fmt.Errorf("timed out waiting for fallback concurrency slot")
	}
}

func (s *Server) releaseFallback() {
	select {
	case <-s.fbSem:
	default:
	}
}
