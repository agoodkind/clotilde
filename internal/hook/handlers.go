package hook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/notify"
	"goodkind.io/clyde/internal/session"
)

func handleStartupOrResume(
	ctx context.Context,
	log *slog.Logger,
	deps sessionStartDeps,
	hookData SessionStartInput,
	store session.Store,
	out io.Writer,
	errOut io.Writer,
) {
	_ = ctx
	sessionName := os.Getenv("CLYDE_SESSION_NAME")

	if sessionName != "" {
		if !store.Exists(sessionName) && hookData.SessionID != "" {
			if session.ValidateName(sessionName) == nil {
				autoAdoptSession(log, deps, store, sessionName, hookData, errOut)
			}
		}

		if err := writeSessionNameToEnv(sessionName); err != nil {
			log.Warn("hook.sessionstart.env_write_failed",
				"component", "hook",
				"subject", "sessionstart",
				"key", "CLYDE_SESSION",
				slog.Any("err", err),
			)
			_, _ = fmt.Fprintf(errOut, "Warning: failed to write session name to env: %v\n", err)
		}

		if hookData.TranscriptPath != "" {
			if err := saveTranscriptPath(store, sessionName, hookData.TranscriptPath); err != nil {
				log.Warn("hook.sessionstart.transcript_save_failed",
					"component", "hook",
					"subject", "sessionstart",
					"session", sessionName,
					slog.Any("err", err),
				)
				_, _ = fmt.Fprintf(errOut, "Warning: failed to save transcript path: %v\n", err)
			} else {
				log.Info("hook.sessionstart.transcript_saved",
					"component", "hook",
					"subject", "sessionstart",
					"session", sessionName,
					"transcript", hookData.TranscriptPath,
				)
			}
		}
	}

	outputContexts(log, store, sessionName, out)
}

func autoAdoptSession(
	log *slog.Logger,
	deps sessionStartDeps,
	store session.Store,
	name string,
	hookData SessionStartInput,
	errOut io.Writer,
) {
	sess := session.NewSession(name, hookData.SessionID)
	sess.Metadata.TranscriptPath = hookData.TranscriptPath

	if wd, err := deps.getwd(); err == nil {
		sess.Metadata.WorkDir = wd
	}
	if root, err := deps.findProjectRoot(); err == nil {
		sess.Metadata.WorkspaceRoot = root
	}

	// Populate DisplayTitle from the Claude Code custom-title entry in
	// the transcript so the TUI surfaces the user-given chat name. The
	// hook handles the pre-named path (CLYDE_SESSION_NAME set), so Name
	// stays authoritative; DisplayTitle is purely decorative.
	if hookData.TranscriptPath != "" {
		if dr, ok := session.ReadTranscriptHeader(hookData.TranscriptPath); ok {
			if dr.CustomTitle != "" {
				sess.Metadata.DisplayTitle = dr.CustomTitle
				log.Debug("hook.sessionstart.display_title_captured",
					"component", "hook",
					"subject", "sessionstart",
					"session", name,
					"display_title", dr.CustomTitle,
				)
			}
			if dr.IsForked {
				sess.Metadata.IsForkedSession = true
				if dr.ForkParentID != "" {
					if parentName, err := findSessionByUUID(store, dr.ForkParentID); err == nil {
						sess.Metadata.ParentSession = parentName
					}
				}
				log.Debug("hook.sessionstart.fork_lineage_captured",
					"component", "hook",
					"subject", "sessionstart",
					"session", name,
					"parent_session", sess.Metadata.ParentSession,
					"parent_session_id", dr.ForkParentID,
				)
			}
		}
	}

	if err := store.Create(sess); err != nil {
		log.Warn("hook.sessionstart.auto_adopt_failed",
			"component", "hook",
			"subject", "sessionstart",
			"session", name,
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintf(errOut, "Warning: auto-adopt failed for '%s': %v\n", name, err)
		return
	}

	log.Info("hook.sessionstart.session_adopted",
		"component", "hook",
		"subject", "sessionstart",
		"session", name,
		"session_id", hookData.SessionID,
		"display_title", sess.Metadata.DisplayTitle,
	)
}

func handleCompact(
	ctx context.Context,
	log *slog.Logger,
	hookData SessionStartInput,
	store session.Store,
	out io.Writer,
	errOut io.Writer,
) error {
	_ = ctx
	sessionName, err := resolveSessionName(hookData, store, true)
	if err != nil {
		log.Warn("hook.sessionstart.resolve_name_failed",
			"component", "hook",
			"subject", "sessionstart",
			"reason", "compact",
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintf(errOut, "Warning: unable to resolve session name for compact: %v\n", err)
		return nil
	}

	if sessionName == "" {
		return nil
	}

	sess, err := store.Get(sessionName)
	if err != nil {
		log.Warn("hook.sessionstart.session_not_found",
			"component", "hook",
			"subject", "sessionstart",
			"reason", "compact",
			"session", sessionName,
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintf(errOut, "Warning: session '%s' not found: %v\n", sessionName, err)
		return nil
	}

	sess.AddPreviousSessionID(hookData.SessionID)
	sess.Metadata.TranscriptPath = hookData.TranscriptPath
	sess.UpdateLastAccessed()

	if err := store.Update(sess); err != nil {
		log.Warn("hook.sessionstart.metadata_update_failed",
			"component", "hook",
			"subject", "sessionstart",
			"session", sessionName,
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintf(errOut, "Warning: failed to update session metadata: %v\n", err)
	}

	if err := writeSessionNameToEnv(sessionName); err != nil {
		log.Warn("hook.sessionstart.env_write_failed",
			"component", "hook",
			"subject", "sessionstart",
			"key", "CLYDE_SESSION",
			"session", sessionName,
			slog.Any("err", err),
		)
		_, _ = fmt.Fprintf(errOut, "Warning: failed to write session name to env: %v\n", err)
	}

	log.Info("hook.sessionstart.compact_handled",
		"component", "hook",
		"subject", "sessionstart",
		"session", sessionName,
		"session_id", hookData.SessionID,
	)

	outputContexts(log, store, sessionName, out)
	return nil
}

func handleClear(
	ctx context.Context,
	log *slog.Logger,
	hookData SessionStartInput,
	store session.Store,
	out io.Writer,
	errOut io.Writer,
) error {
	return handleCompact(ctx, log, hookData, store, out, errOut)
}

func saveTranscriptPath(store session.Store, sessionName, transcriptPath string) error {
	sess, err := store.Get(sessionName)
	if err != nil {
		return fmt.Errorf("session '%s' not found: %w", sessionName, err)
	}

	sess.Metadata.TranscriptPath = transcriptPath
	sess.UpdateLastAccessed()

	if err := store.Update(sess); err != nil {
		return fmt.Errorf("failed to update session metadata: %w", err)
	}

	return nil
}

func outputContexts(log *slog.Logger, store session.Store, sessionName string, out io.Writer) {
	if sessionName != "" {
		log.Info("hook.sessionstart.operator_session_line",
			"component", "hook",
			"subject", "sessionstart",
			"session", sessionName,
		)
		_, _ = fmt.Fprintf(out, "\nSession name: %s\n", sessionName)
	}

	if sessionName != "" {
		sess, err := store.Get(sessionName)
		if err == nil && sess.Metadata.Context != "" {
			log.Info("hook.sessionstart.operator_context_line",
				"component", "hook",
				"subject", "sessionstart",
				"session", sessionName,
			)
			_, _ = fmt.Fprintf(out, "Context: %s\n", sess.Metadata.Context)
		}
	}
}

func defaultDeps(cfg SessionStartConfig) sessionStartDeps {
	deps := sessionStartDeps{
		logRawEvent:     cfg.LogRawEvent,
		getwd:           cfg.Getwd,
		findProjectRoot: cfg.FindProjectRoot,
	}
	if deps.logRawEvent == nil {
		deps.logRawEvent = defaultLogRawEvent
	}
	if deps.getwd == nil {
		deps.getwd = os.Getwd
	}
	if deps.findProjectRoot == nil {
		deps.findProjectRoot = config.FindProjectRoot
	}
	return deps
}

func defaultLogRawEvent(rawJSON []byte, sessionID string) error {
	return notify.LogEvent(rawJSON, sessionID)
}
