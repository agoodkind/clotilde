// Package cmd holds the TUI dashboard, its daemon-backed callbacks,
// the `clyde resume` cobra verb, and the argument-routing helpers
// (ClassifyArgs, ForwardToClaude) used by cmd/clyde/main.go to assemble
// the cobra root.
//
// What lives here:
//
//   - RunDashboard / runPostSessionDashboard (the tcell TUI entrypoint)
//   - TUI callbacks for delete, rename, resume, remote-control,
//     bridges, send, tail, registry, summary, view, model extract
//   - NewResumeCmd (the `clyde resume <name|uuid>` verb)
//   - ClassifyArgs and ForwardToClaude (passthrough routing)
//   - resumeSession / deleteSession helpers shared by the TUI and the
//     resume verb
package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	clydev1 "goodkind.io/clyde/api/clyde/v1"
	"goodkind.io/clyde/internal/config"
	"goodkind.io/clyde/internal/daemon"
	"goodkind.io/clyde/internal/mitm"
	"goodkind.io/clyde/internal/session"
	sessionlifecycle "goodkind.io/clyde/internal/session/lifecycle"
	"goodkind.io/clyde/internal/ui"
)

// RunDashboard is the entrypoint for `clyde` with no subcommand. It
// boots the tcell TUI dashboard for managing existing sessions
// (resume, delete, rename, view, remote-control toggle, send-to,
// tail-transcript). New sessions from the TUI launch `claude` with
// CLYDE_SESSION_NAME set; the SessionStart hook adopts the row.
func RunDashboard(cmd *cobra.Command, args []string) int {
	// Non-interactive (piped) invocation: forward to real claude.
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return ForwardToClaude(os.Args[1:])
	}

	// Non-TTY stdout: show help. Avoids drawing the TUI into a pipe.
	if !isatty.IsTerminal(os.Stdout.Fd()) {
		_ = cmd.Help()
		return 0
	}

	return runDashboardTUI()
}

// runDashboardTUI opens the session dashboard. Caller must ensure stdin and
// stdout are TTYs (see RunDashboard).
func runDashboardTUI() int {
	daemon.NudgeDiscoveryScan()
	cmdUILog.Logger().Info("dashboard.opened", "component", "tui")

	dashboardCwd, _ := os.Getwd()
	cb := buildAppCallbacks(dashboardCwd)
	app := ui.NewApp(nil, cb, dashboardAppOptions(dashboardCwd, "", consumeTUIReturnSession()))

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		cmdUILog.Logger().Error("dashboard.tui_error",
			"component", "tui",
			"err", err,
		)
		return 1
	}
	cmdUILog.Logger().Info("dashboard.closed", "component", "tui")
	return 0
}

// RunBasedirLaunch opens the dashboard biased toward one workspace root.
// The caller is responsible for only invoking this for an existing directory.
func RunBasedirLaunch(basedir string) int {
	if !isatty.IsTerminal(os.Stdin.Fd()) || !isatty.IsTerminal(os.Stdout.Fd()) {
		return ForwardToClaude(os.Args[1:])
	}
	daemon.NudgeDiscoveryScan()
	canonical := session.CanonicalWorkspaceRoot(basedir)
	cmdUILog.Logger().Info("dashboard.basedir.opened",
		"component", "tui",
		"basedir", canonical,
	)

	dashboardCwd, _ := os.Getwd()
	cb := buildAppCallbacks(dashboardCwd)
	app := ui.NewApp(nil, cb, dashboardAppOptions(canonical, canonical, consumeTUIReturnSession()))

	if err := app.Run(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		cmdUILog.Logger().Error("dashboard.basedir.tui_error",
			"component", "tui",
			"basedir", canonical,
			"err", err,
		)
		return 1
	}
	cmdUILog.Logger().Info("dashboard.basedir.closed",
		"component", "tui",
		"basedir", canonical,
	)
	return 0
}

func dashboardAppOptions(launchCWD, launchBasedir string, returnTo *session.Session) ui.AppOptions {
	return ui.AppOptions{
		DashboardLaunchCWD: launchCWD,
		LaunchBasedir:      launchBasedir,
		ReturnTo:           returnTo,
	}
}

func consumeTUIReturnSession() *session.Session {
	sessionID := strings.TrimSpace(os.Getenv(ui.EnvTUIReturnSessionID))
	sessionName := strings.TrimSpace(os.Getenv(ui.EnvTUIReturnSessionName))
	_ = os.Unsetenv(ui.EnvTUIReturnSessionID)
	_ = os.Unsetenv(ui.EnvTUIReturnSessionName)
	query := sessionID
	if query == "" {
		query = sessionName
	}
	if query == "" {
		return nil
	}
	store, err := session.NewGlobalFileStoreReadOnly()
	if err != nil {
		cmdLog.Warn("dashboard.return_session.store_failed",
			"component", "tui",
			"session", sessionName,
			"session_id", sessionID,
			"err", err)
		return nil
	}
	sess, err := store.Resolve(query)
	if err != nil || sess == nil {
		cmdLog.Warn("dashboard.return_session.resolve_failed",
			"component", "tui",
			"session", sessionName,
			"session_id", sessionID,
			"err", err)
		return nil
	}
	cmdUILog.Logger().Info("dashboard.return_session.restored",
		"component", "tui",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID())
	return sess
}

// buildAppCallbacks wires store + helpers into a ui.AppCallbacks.
// dashboardLaunchCWD is the process cwd when RunDashboard started; it
// is the default basedir for "new session" without picking a folder.
func buildAppCallbacks(dashboardLaunchCWD string) ui.AppCallbacks {
	openStore := func() (session.Store, error) {
		return session.NewGlobalFileStore()
	}
	return ui.AppCallbacks{
		ListSessions: func() (ui.SessionSnapshot, error) {
			resp, err := daemon.ListSessionsViaDaemon(context.Background())
			if err != nil {
				return ui.SessionSnapshot{}, err
			}
			return sessionSnapshotFromProto(resp), nil
		},
		LoadStats: func() (ui.DashboardStats, error) {
			return loadDashboardStats(context.Background())
		},
		SubscribeProviderStats: func() (<-chan ui.ProviderStats, func(), error) {
			raw, cancel, err := daemon.SubscribeProviderStats(context.Background())
			if err != nil {
				return nil, nil, err
			}
			out := make(chan ui.ProviderStats, 8)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.provider_stats.forwarder_panic",
							"component", "tui",
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer close(out)
				for ev := range raw {
					if ev == nil || ev.GetStats() == nil {
						continue
					}
					stats := providerStatsFromProto([]*clydev1.ProviderStats{ev.GetStats()})
					if len(stats) == 0 {
						continue
					}
					out <- stats[0]
				}
			}()
			return out, cancel, nil
		},
		RestartDaemon: func() error {
			return daemon.RestartManagedDaemon(context.Background())
		},
		StartSessionWithBasedir: func(basedir string) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			return startNewSessionInDir(basedir, store, dashboardLaunchCWD, false)
		},
		StartSessionWithBasedirRC: func(basedir string, enableRC bool) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			return startNewSessionInDir(basedir, store, dashboardLaunchCWD, enableRC)
		},
		StartRemoteSession: func(basedir string, incognito bool) (string, string, error) {
			resp, err := daemon.StartRemoteSessionViaDaemon(context.Background(), "", basedir, incognito)
			if err != nil {
				return "", "", err
			}
			return resp.GetSessionName(), resp.GetSessionId(), nil
		},
		ResumeSession: func(sess *session.Session) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			return resumeSession(sess, store, true)
		},
		DeleteSession: func(sess *session.Session) error {
			outcome, err := daemon.DeleteSessionViaDaemonOutcome(context.Background(), sess.Name)
			if outcome != daemon.LifecycleOutcomeReady {
				return daemonLifecycleError("delete", outcome, err)
			}
			return nil
		},
		RenameSession: func(sess *session.Session) (string, error) {
			newName := sess.Name
			oldName := sess.Metadata.Name
			if oldName == "" || oldName == newName {
				return newName, nil
			}
			outcome, err := daemon.RenameSessionViaDaemonOutcome(context.Background(), oldName, newName)
			if outcome != daemon.LifecycleOutcomeReady {
				return newName, daemonLifecycleError("rename", outcome, err)
			}
			return newName, nil
		},
		SetBasedir: func(sess *session.Session, newPath string) error {
			if sess == nil || sess.Name == "" {
				return fmt.Errorf("nil session")
			}
			outcome, err := daemon.UpdateSessionWorkspaceRootViaDaemonOutcome(context.Background(), sess.Name, newPath)
			if outcome != daemon.LifecycleOutcomeReady {
				return daemonLifecycleError("update_session_workspace_root", outcome, err)
			}
			return nil
		},
		RefreshSummary: func(sess *session.Session, onDone func(*session.Session)) error {
			if sess == nil || sess.Name == "" {
				return fmt.Errorf("nil session")
			}
			go func(name, workspaceRoot string) {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.refresh_summary.worker_panic",
							"component", "tui",
							"session", name,
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer func() {
					if onDone != nil {
						onDone(nil)
					}
				}()
				resp, err := daemon.GetSessionDetailViaDaemon(context.Background(), name)
				if err != nil {
					return
				}
				all := resp.GetAllMessages()
				if len(all) == 0 {
					return
				}
				start := 0
				if len(all) > 100 {
					start = len(all) - 100
				}
				messages := make([]string, 0, len(all)-start)
				for _, m := range all[start:] {
					role := strings.TrimSpace(m.GetRole())
					text := strings.TrimSpace(m.GetText())
					if role == "" || text == "" {
						continue
					}
					roleLabel := "User"
					if strings.EqualFold(role, "assistant") {
						roleLabel = "Assistant"
					}
					runes := []rune(text)
					if len(runes) > 300 {
						text = string(runes[:300])
					}
					messages = append(messages, fmt.Sprintf("[%s] %s", roleLabel, text))
				}
				if len(messages) == 0 {
					return
				}
				client, err := daemon.ConnectOrStart(context.Background())
				if err != nil {
					return
				}
				defer func() { _ = client.Close() }()
				_ = client.UpdateContext(name, workspaceRoot, messages)
			}(sess.Name, sess.Metadata.WorkspaceRoot)
			return nil
		},
		ViewContent: func(sess *session.Session) string {
			resp, err := daemon.GetSessionDetailViaDaemon(context.Background(), sess.Name)
			if err != nil || len(resp.GetAllMessages()) == 0 {
				return ""
			}
			var b strings.Builder
			for _, m := range resp.GetAllMessages() {
				if m.GetRole() == "" || m.GetText() == "" {
					continue
				}
				b.WriteString(strings.ToUpper(m.GetRole()))
				b.WriteString(":\n")
				b.WriteString(m.GetText())
				b.WriteString("\n\n")
			}
			return strings.TrimSpace(b.String())
		},
		ExportSession: func(sess *session.Session, req ui.SessionExportRequest) ([]byte, error) {
			rpcReq := exportRequestToProto(sess, req)
			resp, err := daemon.ExportSessionViaDaemon(context.Background(), rpcReq)
			if err != nil {
				return nil, err
			}
			return resp.GetBody(), nil
		},
		LoadExportStats: func(sess *session.Session) (ui.SessionExportStats, error) {
			if sess == nil {
				return ui.SessionExportStats{}, fmt.Errorf("nil session")
			}
			resp, err := daemon.GetSessionExportStatsViaDaemon(context.Background(), sess.Name)
			if err != nil {
				return ui.SessionExportStats{}, err
			}
			return exportStatsFromProto(resp), nil
		},
		SubscribeRegistry: func() (<-chan ui.SessionEvent, func(), error) {
			raw, cancel, err := daemon.SubscribeRegistry(context.Background())
			if err != nil {
				return nil, nil, err
			}
			out := make(chan ui.SessionEvent, 8)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.registry.forwarder_panic",
							"component", "tui",
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer close(out)
				for ev := range raw {
					out <- sessionEventFromProto(ev)
				}
			}()
			return out, cancel, nil
		},
		SetRemoteControl: func(sess *session.Session, enabled bool) error {
			if sess == nil || sess.Name == "" {
				return fmt.Errorf("nil session")
			}
			outcome, err := daemon.UpdateSessionRemoteControlViaDaemonOutcome(context.Background(), sess.Name, enabled)
			if outcome != daemon.LifecycleOutcomeReady {
				return daemonLifecycleError("update_session_remote_control", outcome, err)
			}
			return nil
		},
		SetGlobalRemoteControl: func(enabled bool) error {
			outcome, err := daemon.UpdateGlobalRemoteControlViaDaemonOutcome(context.Background(), enabled)
			if outcome != daemon.LifecycleOutcomeReady {
				return daemonLifecycleError("update_global_remote_control", outcome, err)
			}
			return nil
		},
		LoadConfigControls: func() ([]ui.ConfigControl, error) {
			raw, err := daemon.ListConfigControlsViaDaemon(context.Background())
			if err != nil {
				return nil, err
			}
			return configControlsFromProto(raw), nil
		},
		UpdateConfigControl: func(key, value string) error {
			_, err := daemon.UpdateConfigControlViaDaemon(context.Background(), key, value)
			return err
		},
		ListBridges: func() ([]ui.Bridge, error) {
			raw, err := daemon.ListBridgesViaDaemon(context.Background())
			if err != nil {
				return nil, err
			}
			out := make([]ui.Bridge, 0, len(raw))
			for _, b := range raw {
				out = append(out, ui.Bridge{
					SessionID:       b.GetSessionId(),
					PID:             b.GetPid(),
					BridgeSessionID: b.GetBridgeSessionId(),
					URL:             b.GetUrl(),
				})
			}
			return out, nil
		},
		SendToSession: func(sessionID, text string) error {
			ok, err := daemon.SendToSessionViaDaemon(context.Background(), sessionID, text)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("session not listening on inject socket")
			}
			return nil
		},
		TailTranscript: func(sessionID string, startOffset int64) (<-chan ui.TranscriptEntry, func(), error) {
			raw, cancel, err := daemon.TailTranscriptViaDaemon(context.Background(), sessionID, startOffset)
			if err != nil {
				return nil, nil, err
			}
			out := make(chan ui.TranscriptEntry, 32)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.transcript.forwarder_panic",
							"component", "tui",
							"session_id", sessionID,
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer close(out)
				for ln := range raw {
					ts := time.Time{}
					if ln.GetTimestampNanos() > 0 {
						ts = time.Unix(0, ln.GetTimestampNanos())
					}
					out <- ui.TranscriptEntry{
						ByteOffset: ln.GetByteOffset(),
						RawJSONL:   ln.GetRawJsonl(),
						Role:       ln.GetRole(),
						Text:       ln.GetText(),
						Timestamp:  ts,
					}
				}
			}()
			return out, cancel, nil
		},
		CompactPreview: func(req ui.CompactRunRequest) (<-chan ui.CompactEvent, <-chan error, func(), error) {
			raw, done, cancel, err := daemon.CompactPreviewViaDaemon(context.Background(), daemon.CompactRunOptions{
				SessionName:    req.SessionName,
				TargetTokens:   req.TargetTokens,
				ReservedTokens: req.ReservedTokens,
				Model:          req.Model,
				ModelExplicit:  req.ModelExplicit,
				Thinking:       req.Thinking,
				Images:         req.Images,
				Tools:          req.Tools,
				Chat:           req.Chat,
				Summarize:      req.Summarize,
				Force:          req.Force,
			})
			if err != nil {
				return nil, nil, nil, err
			}
			out := make(chan ui.CompactEvent, 64)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.compact_preview.forwarder_panic",
							"component", "tui",
							"session", req.SessionName,
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer close(out)
				for ev := range raw {
					out <- compactEventFromProto(ev)
				}
			}()
			return out, done, cancel, nil
		},
		CompactApply: func(req ui.CompactRunRequest) (<-chan ui.CompactEvent, <-chan error, func(), error) {
			raw, done, cancel, err := daemon.CompactApplyViaDaemon(context.Background(), daemon.CompactRunOptions{
				SessionName:    req.SessionName,
				TargetTokens:   req.TargetTokens,
				ReservedTokens: req.ReservedTokens,
				Model:          req.Model,
				ModelExplicit:  req.ModelExplicit,
				Thinking:       req.Thinking,
				Images:         req.Images,
				Tools:          req.Tools,
				Chat:           req.Chat,
				Summarize:      req.Summarize,
				Force:          req.Force,
			})
			if err != nil {
				return nil, nil, nil, err
			}
			out := make(chan ui.CompactEvent, 64)
			go func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						cmdUILog.Logger().Error("dashboard.compact_apply.forwarder_panic",
							"component", "tui",
							"session", req.SessionName,
							"err", fmt.Errorf("panic: %v", recovered),
						)
					}
				}()
				defer close(out)
				for ev := range raw {
					out <- compactEventFromProto(ev)
				}
			}()
			return out, done, cancel, nil
		},
		CompactUndo: func(sessionName string) (*ui.CompactUndoResult, error) {
			resp, err := daemon.CompactUndoViaDaemon(context.Background(), sessionName)
			if err != nil {
				return nil, err
			}
			return &ui.CompactUndoResult{
				AppliedAt:     resp.GetAppliedAt(),
				BoundaryUUID:  resp.GetBoundaryUuid(),
				SyntheticUUID: resp.GetSyntheticUuid(),
			}, nil
		},
		GetSessionDetail: func(sess *session.Session) (ui.SessionDetail, error) {
			resp, err := daemon.GetSessionDetailViaDaemon(context.Background(), sess.Name)
			if err != nil {
				return ui.SessionDetail{}, err
			}
			return sessionDetailFromProto(resp), nil
		},
	}
}

func compactEventFromProto(ev *clydev1.CompactEvent) ui.CompactEvent {
	out := ui.CompactEvent{}
	switch ev.GetKind() {
	case clydev1.CompactEvent_KIND_STATUS:
		out.Kind = "status"
		out.Message = ev.GetMessage()
	case clydev1.CompactEvent_KIND_UPFRONT:
		upfront := ev.GetUpfront()
		out.Kind = "upfront"
		out.Upfront = &ui.CompactUpfront{
			SessionName:    upfront.GetSessionName(),
			SessionID:      upfront.GetSessionId(),
			Model:          upfront.GetModel(),
			CurrentTotal:   int(upfront.GetCurrentTotal()),
			MaxTokens:      int(upfront.GetMaxTokens()),
			TargetTokens:   int(upfront.GetTargetTokens()),
			ReservedTokens: int(upfront.GetReservedTokens()),
		}
	case clydev1.CompactEvent_KIND_ITERATION:
		it := ev.GetIteration()
		out.Kind = "iteration"
		out.Iteration = &ui.CompactIteration{
			Iteration: int(it.GetIteration()),
			Step:      it.GetStep(),
			CtxTotal:  int(it.GetCtxTotal()),
			Delta:     int(it.GetDelta()),
		}
	case clydev1.CompactEvent_KIND_FINAL:
		fin := ev.GetFinal()
		out.Kind = "final"
		out.Final = &ui.CompactFinal{
			FinalTail:      int(fin.GetFinalTail()),
			TargetTokens:   int(fin.GetTargetTokens()),
			StaticFloor:    int(fin.GetStaticFloor()),
			ReservedTokens: int(fin.GetReservedTokens()),
		}
	case clydev1.CompactEvent_KIND_APPLY_MUTATION:
		m := ev.GetApplyMutation()
		out.Kind = "apply_mutation"
		out.ApplyMutation = &ui.CompactApplyMutation{
			BoundaryUUID:  m.GetBoundaryUuid(),
			SyntheticUUID: m.GetSyntheticUuid(),
			SnapshotPath:  m.GetSnapshotPath(),
			LedgerPath:    m.GetLedgerPath(),
		}
	default:
		out.Kind = "status"
		out.Message = "received compact stream update"
	}
	return out
}

func sessionSnapshotFromProto(resp *clydev1.ListSessionsResponse) ui.SessionSnapshot {
	out := ui.SessionSnapshot{
		Sessions:            make([]*session.Session, 0, len(resp.GetSessions())),
		Models:              make(map[string]string, len(resp.GetSessions())),
		RemoteControl:       make(map[string]bool, len(resp.GetSessions())),
		MessageCounts:       make(map[string]int, len(resp.GetSessions())),
		ContextStates:       make(map[string]ui.SessionContextState, len(resp.GetSessions())),
		GlobalRemoteControl: resp.GetGlobalRemoteControl(),
	}
	for _, raw := range resp.GetSessions() {
		sess, model, remoteControl, messageCount, contextState, bridge := sessionSummaryFromProto(raw)
		out.Sessions = append(out.Sessions, sess)
		out.Models[sess.Name] = model
		out.RemoteControl[sess.Name] = remoteControl
		out.MessageCounts[sess.Name] = messageCount
		out.ContextStates[sess.Name] = contextState
		if bridge != nil {
			out.Bridges = append(out.Bridges, *bridge)
		}
	}
	return out
}

func sessionSummaryFromProto(raw *clydev1.SessionSummary) (*session.Session, string, bool, int, ui.SessionContextState, *ui.Bridge) {
	sess := &session.Session{
		Name: raw.GetName(),
		Metadata: session.Metadata{
			Name:                 raw.GetMetadataName(),
			SessionID:            raw.GetSessionId(),
			TranscriptPath:       raw.GetTranscriptPath(),
			WorkDir:              raw.GetWorkDir(),
			Created:              timeFromNanos(raw.GetCreatedNanos()),
			LastAccessed:         timeFromNanos(raw.GetLastActivityNanos()),
			ParentSession:        raw.GetParentSession(),
			IsForkedSession:      raw.GetIsForkedSession(),
			IsIncognito:          raw.GetIsIncognito(),
			PreviousSessionIDs:   append([]string(nil), raw.GetPreviousSessionIds()...),
			Context:              raw.GetContext(),
			HasCustomOutputStyle: raw.GetHasCustomOutputStyle(),
			WorkspaceRoot:        raw.GetWorkspaceRoot(),
			ContextMessageCount:  int(raw.GetContextMessageCount()),
			DisplayTitle:         raw.GetDisplayTitle(),
		},
	}
	if sess.Metadata.Name == "" {
		sess.Metadata.Name = sess.Name
	}
	contextState := ui.SessionContextState{
		Usage: ui.SessionContextUsage{
			TotalTokens:    int(raw.GetContextTotalTokens()),
			MaxTokens:      int(raw.GetContextMaxTokens()),
			Percentage:     int(raw.GetContextPercentage()),
			MessagesTokens: int(raw.GetContextMessagesTokens()),
		},
		Loaded: raw.GetContextUsageLoaded(),
		Status: raw.GetContextUsageStatus(),
	}

	var bridge *ui.Bridge
	if b := raw.GetBridge(); b != nil {
		bridge = &ui.Bridge{
			SessionID:       b.GetSessionId(),
			PID:             b.GetPid(),
			BridgeSessionID: b.GetBridgeSessionId(),
			URL:             b.GetUrl(),
		}
	}
	return sess, raw.GetModel(), raw.GetRemoteControl(), int(raw.GetMessageCount()), contextState, bridge
}

func sessionEventFromProto(ev *clydev1.SubscribeRegistryResponse) ui.SessionEvent {
	out := ui.SessionEvent{
		Kind:            strings.TrimPrefix(ev.GetKind().String(), "KIND_"),
		SessionName:     ev.GetSessionName(),
		SessionID:       ev.GetSessionId(),
		OldName:         ev.GetOldName(),
		BridgeSessionID: ev.GetBridgeSessionId(),
		BridgeURL:       ev.GetBridgeUrl(),
		BinaryPath:      ev.GetBinaryPath(),
		BinaryReason:    ev.GetBinaryReason(),
		BinaryHash:      ev.GetBinaryHash(),
	}
	if raw := ev.GetSessionSummary(); raw != nil {
		sess, model, remoteControl, messageCount, contextState, bridge := sessionSummaryFromProto(raw)
		out.Session = sess
		out.Model = model
		out.RemoteControl = remoteControl
		out.MessageCount = messageCount
		out.ContextState = &contextState
		out.Bridge = bridge
	}
	if ev.GetKind() == clydev1.SubscribeRegistryResponse_KIND_GLOBAL_SETTINGS_UPDATED {
		globalRC := ev.GetGlobalRemoteControl()
		out.GlobalRemoteControl = &globalRC
	}
	return out
}

func sessionDetailFromProto(resp *clydev1.GetSessionDetailResponse) ui.SessionDetail {
	out := ui.SessionDetail{
		Model:                 resp.GetModel(),
		TotalMessages:         int(resp.GetTotalMessages()),
		VisibleTokensEstimate: int(resp.GetVisibleTokensEstimate()),
		LastMessageTokens:     int(resp.GetLastMessageTokens()),
		CompactionCount:       int(resp.GetCompactionCount()),
		LastPreCompactTokens:  int(resp.GetLastPreCompactTokens()),
		TranscriptSizeBytes:   resp.GetTranscriptSizeBytes(),
		TranscriptStatsLoaded: true,
	}
	for _, m := range resp.GetRecentMessages() {
		out.Messages = append(out.Messages, ui.DetailMessage{
			Role:      m.GetRole(),
			Text:      m.GetText(),
			Timestamp: timeFromNanos(m.GetTimestampNanos()),
		})
	}
	for _, m := range resp.GetAllMessages() {
		out.AllMessages = append(out.AllMessages, ui.DetailMessage{
			Role:      m.GetRole(),
			Text:      m.GetText(),
			Timestamp: timeFromNanos(m.GetTimestampNanos()),
		})
	}
	for _, t := range resp.GetTools() {
		out.Tools = append(out.Tools, ui.ToolUse{Name: t.GetName(), Count: int(t.GetCount())})
	}
	return out
}

func exportStatsFromProto(resp *clydev1.GetSessionExportStatsResponse) ui.SessionExportStats {
	if resp == nil {
		return ui.SessionExportStats{}
	}
	return ui.SessionExportStats{
		VisibleTokensEstimate: int(resp.GetVisibleTokensEstimate()),
		VisibleMessages:       int(resp.GetVisibleMessages()),
		UserMessages:          int(resp.GetUserMessages()),
		AssistantMessages:     int(resp.GetAssistantMessages()),
		ToolResultMessages:    int(resp.GetToolResultMessages()),
		ToolCalls:             int(resp.GetToolCalls()),
		SystemPrompts:         int(resp.GetSystemPrompts()),
		Compactions:           int(resp.GetCompactions()),
		TranscriptSizeBytes:   resp.GetTranscriptSizeBytes(),
	}
}

func exportRequestToProto(sess *session.Session, req ui.SessionExportRequest) *clydev1.ExportSessionRequest {
	sessionName := strings.TrimSpace(req.SessionName)
	if sessionName == "" && sess != nil {
		sessionName = sess.Name
	}
	return &clydev1.ExportSessionRequest{
		SessionName:            sessionName,
		Format:                 exportFormatToProto(req.Format),
		HistoryStart:           int32(req.HistoryStart),
		WhitespaceCompression:  exportWhitespaceToProto(req.WhitespaceCompression),
		IncludeChat:            req.IncludeChat,
		IncludeThinking:        req.IncludeThinking,
		IncludeSystemPrompts:   req.IncludeSystemPrompts,
		IncludeToolCalls:       req.IncludeToolCalls,
		IncludeToolOutputs:     req.IncludeToolOutputs,
		IncludeRawJsonMetadata: req.IncludeRawJSONMetadata,
	}
}

func exportFormatToProto(format ui.SessionExportFormat) clydev1.SessionExportFormat {
	switch format {
	case ui.SessionExportHTML:
		return clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_HTML
	case ui.SessionExportJSON:
		return clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_JSON
	case ui.SessionExportPlainText:
		return clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_PLAIN_TEXT
	case ui.SessionExportMarkdown:
		fallthrough
	default:
		return clydev1.SessionExportFormat_SESSION_EXPORT_FORMAT_MARKDOWN
	}
}

func exportWhitespaceToProto(mode ui.SessionExportWhitespaceCompression) clydev1.SessionExportWhitespaceCompression {
	switch mode {
	case ui.SessionExportWhitespacePreserve:
		return clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_PRESERVE
	case ui.SessionExportWhitespaceCompact:
		return clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_COMPACT
	case ui.SessionExportWhitespaceDense:
		return clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_DENSE
	case ui.SessionExportWhitespaceTidy:
		fallthrough
	default:
		return clydev1.SessionExportWhitespaceCompression_SESSION_EXPORT_WHITESPACE_COMPRESSION_TIDY
	}
}

func configControlsFromProto(raw []*clydev1.ConfigControl) []ui.ConfigControl {
	out := make([]ui.ConfigControl, 0, len(raw))
	for _, control := range raw {
		if control == nil {
			continue
		}
		entry := ui.ConfigControl{
			Key:          control.GetKey(),
			Section:      control.GetSection(),
			Label:        control.GetLabel(),
			Description:  control.GetDescription(),
			Type:         strings.ToLower(strings.TrimPrefix(control.GetType().String(), "CONFIG_CONTROL_TYPE_")),
			Value:        control.GetValue(),
			DefaultValue: control.GetDefaultValue(),
			Sensitive:    control.GetSensitive(),
			ReadOnly:     control.GetReadOnly(),
		}
		for _, opt := range control.GetOptions() {
			entry.Options = append(entry.Options, ui.ConfigControlOption{
				Value:       opt.GetValue(),
				Label:       opt.GetLabel(),
				Description: opt.GetDescription(),
			})
		}
		out = append(out, entry)
	}
	return out
}

func timeFromNanos(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// ForwardToClaude runs the real claude binary (bypassing the shell
// alias) with the given args, inheriting stdin/stdout/stderr. Returns
// the exit code. Used by the dispatch path and by RunDashboard's
// piped-input shortcut.
func ForwardToClaude(args []string) int {
	return runClaudeWithEnv(args, applyClaudeMITMEnv(os.Environ()))
}

func runClaudeWithEnv(args []string, env []string) int {
	claudePath, err := exec.LookPath("claude")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "clyde: cannot find claude binary: %v\n", err)
		cmdUILog.Logger().Error("forward.claude_not_found",
			"component", "cli",
			"err", err,
		)
		return 1
	}
	cmdUILog.Logger().Debug("forward.claude.invoked",
		"component", "cli",
		"argc", len(args),
	)
	c := exec.Command(claudePath, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Env = env
	if runErr := c.Run(); runErr != nil {
		if c.ProcessState != nil {
			return c.ProcessState.ExitCode()
		}
		return 1
	}
	return 0
}

func applyClaudeMITMEnv(env []string) []string {
	cfg, err := config.LoadGlobalOrDefault()
	if err != nil {
		return env
	}
	extra, err := mitm.ClaudeEnv(context.Background(), cfg.MITM, slog.Default())
	if err != nil {
		cmdLog.Warn("forward.mitm.claude_env_failed", "component", "cli", "err", err)
		return env
	}
	out := append([]string(nil), env...)
	for key, value := range extra {
		out = withEnvValue(out, key, value)
	}
	return out
}

// claudePassthroughFirstArgSkipsPostSessionTUI lists argv[0] values that
// usually run a one-shot or long-lived non-dashboard claude subcommand (see
// claude-code entrypoints/cli.tsx fast paths and main.tsx program.command
// registrations). When clyde forwards to claude and these are the first
// token, skip the post-claude TUI (same intent as api / print).
var claudePassthroughFirstArgSkipsPostSessionTUI = map[string]bool{
	"agents":             true,
	"assistant":          true,
	"attach":             true,
	"auth":               true,
	"auto-mode":          true,
	"bridge":             true,
	"daemon":             true,
	"doctor":             true,
	"environment-runner": true,
	"install":            true,
	"kill":               true,
	"logs":               true,
	"plugin":             true,
	"plugins":            true,
	"ps":                 true,
	"rc":                 true,
	"remote":             true,
	"remote-control":     true,
	"self-hosted-runner": true,
	"server":             true,
	"setup-token":        true,
	"sync":               true,
	"update":             true,
	"upgrade":            true,
}

// passthroughSkipsPostSessionTUI reports args that should not open the
// dashboard after claude exits (non-interactive or API-style invocations).
func passthroughSkipsPostSessionTUI(args []string) bool {
	if len(args) == 0 {
		return false
	}
	if args[0] == "api" {
		return true
	}
	if _, skip := claudePassthroughFirstArgSkipsPostSessionTUI[args[0]]; skip {
		return true
	}
	for _, a := range args {
		if a == "-p" || a == "--print" {
			return true
		}
	}
	return false
}

// ForwardToClaudeThenDashboard runs claude like ForwardToClaude, but for an
// interactive terminal it assigns CLYDE_SESSION_NAME when unset so the
// SessionStart hook adopts the session, then opens the TUI when claude exits.
// Pipe and print-style invocations behave like ForwardToClaude only.
func ForwardToClaudeThenDashboard(args []string) int {
	if !shouldOpenDashboardAfterPassthrough(args) {
		return ForwardToClaude(args)
	}
	env := withEnvValue(os.Environ(), "CLYDE_LAUNCH_CWD", currentWorkingDirectory())
	if os.Getenv("CLYDE_SESSION_NAME") == "" {
		store, serr := session.NewGlobalFileStore()
		if serr == nil {
			name, nerr := nextChatSessionName(store)
			if nerr == nil {
				env = append(env, "CLYDE_SESSION_NAME="+name)
				cmdUILog.Logger().Info("forward.passthrough_wrapped",
					"component", "cli",
					"session", name,
				)
			}
		}
	}
	_ = runClaudeWithEnv(args, env)
	_ = runDashboardTUI()
	return 0
}

func shouldOpenDashboardAfterPassthrough(args []string) bool {
	if passthroughSkipsPostSessionTUI(args) {
		return false
	}
	return isatty.IsTerminal(os.Stdin.Fd()) && isatty.IsTerminal(os.Stdout.Fd())
}

func currentWorkingDirectory() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(wd)
}

func withEnvValue(env []string, key, value string) []string {
	if key == "" || value == "" {
		return env
	}
	prefix := key + "="
	out := append([]string(nil), env...)
	for i, item := range out {
		if strings.HasPrefix(item, prefix) {
			out[i] = prefix + value
			return out
		}
	}
	return append(out, prefix+value)
}

// nextChatSessionName returns a new registry-safe chat-* name that does not
// collide with existing session directories.
func nextChatSessionName(store session.Store) (string, error) {
	list, err := store.List()
	if err != nil {
		return "", err
	}
	taken := make(map[string]bool, len(list))
	for _, s := range list {
		taken[s.Name] = true
	}
	base := "chat-" + currentTime().UTC().Format("20060102-150405")
	name := session.UniqueName(base, taken)
	if taken[name] {
		return "", fmt.Errorf("could not allocate a unique session name")
	}
	return name, nil
}

// startNewSessionInDir launches the default interactive provider for a new named
// session in workDir.
// basedir may be empty; dashboardFallbackCWD is used when the trimmed path is empty.
func startNewSessionInDir(basedir string, store session.Store, dashboardFallbackCWD string, enableRemoteControl bool) error {
	workDir := strings.TrimSpace(basedir)
	if workDir == "" {
		workDir = strings.TrimSpace(dashboardFallbackCWD)
	}
	if workDir == "" {
		wd, err := os.Getwd()
		if err != nil {
			cmdLog.Warn("session.new.workdir_resolve_failed",
				"component", "cli",
				"err", err,
			)
			return fmt.Errorf("resolve working directory: %w", err)
		}
		workDir = wd
	}

	name, err := nextChatSessionName(store)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "Starting new session %q in %s\n\n", name, workDir)
	cmdUILog.Logger().Info("session.new.started",
		"component", "cli",
		"session", name,
		"workdir", workDir,
		"remote_control", enableRemoteControl,
	)

	runtime, err := sessionlifecycle.Default(store)
	if err != nil {
		return err
	}
	err = runtime.StartInteractive(context.Background(), session.StartRequest{
		SessionName: name,
		Launch: session.LaunchOptions{
			WorkDir:             workDir,
			Intent:              session.LaunchIntentNewSession,
			EnableRemoteControl: enableRemoteControl,
		},
	})
	if err != nil {
		return err
	}
	sess, gerr := store.Get(name)
	if gerr == nil && sess != nil {
		if fs, ok := store.(*session.FileStore); ok {
			autoUpdateContext(fs, sess)
		}
		printResumeInstructions(sess)
	}
	return nil
}

// resumeSession resumes an existing clyde session through the provider-owned
// lifecycle, reattaching its workspace add-dir if the user invoked from a
// different cwd. Shared by the resume cobra verb and the TUI dashboard's
// resume callback.
func resumeSession(sess *session.Session, store session.Store, allowSelfReload bool) error {
	currentWorkDir := ""
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		currentWorkDir = cwd
	}

	_, _ = fmt.Fprintf(os.Stdout, "Resuming session '%s' (%s)\n\n", sess.Name, sess.Metadata.ProviderSessionID())
	_, _ = fmt.Fprintln(os.Stdout, "Dashboard is suspended while Claude runs. Exit Claude to return.")
	_, _ = fmt.Fprintln(os.Stdout)
	cmdUILog.Logger().Info("session.resume.started",
		"component", "cli",
		"session", sess.Name,
		"session_id", sess.Metadata.ProviderSessionID(),
	)

	runtime, err := sessionlifecycle.ForSession(sess, store)
	if err != nil {
		return err
	}
	err = runtime.ResumeInteractive(context.Background(), session.ResumeRequest{
		Session: sess,
		Options: session.ResumeOptions{
			CurrentWorkDir:   currentWorkDir,
			EnableSelfReload: allowSelfReload,
		},
	})
	if fs, ok := store.(*session.FileStore); ok {
		autoUpdateContext(fs, sess)
	}
	printResumeInstructions(sess)
	return err
}

func daemonLifecycleError(action string, outcome daemon.LifecycleOutcome, err error) error {
	if err == nil {
		return fmt.Errorf("daemon %s %s", action, outcome)
	}
	cmdLog.Warn("daemon.lifecycle.failed",
		"component", "cli",
		"action", action,
		"outcome", outcome,
		"err", err,
	)
	return fmt.Errorf("daemon %s %s: %w", action, outcome, err)
}
