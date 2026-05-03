package codex

import (
	"context"
	"fmt"
	"strings"
)

// LiveRuntime is Clyde's provider-private contract for driving a live Codex
// conversation without exposing Codex transport details to daemon, UI, or
// generic session code.
type LiveRuntime interface {
	Start(context.Context, LiveStartRequest) (*LiveSession, error)
	Attach(context.Context, LiveAttachRequest) (*LiveSession, error)
	Send(context.Context, LiveSendRequest) (*LiveTurn, error)
	Stream(context.Context, LiveStreamRequest) (<-chan LiveEvent, error)
	Stop(context.Context, LiveStopRequest) error
	Close() error
}

type LiveStartRequest struct {
	WorkDir               string
	Model                 string
	ModelProvider         string
	DeveloperInstructions string
	BaseInstructions      string
	SessionName           string
	Ephemeral             bool
}

type LiveAttachRequest struct {
	ThreadID string
}

type LiveSendRequest struct {
	ThreadID string
	Text     string
	WorkDir  string
	Model    string
}

type LiveStreamRequest struct {
	ThreadID string
	TurnID   string
}

type LiveStopRequest struct {
	ThreadID string
	TurnID   string
}

type LiveSession struct {
	ThreadID string
	WorkDir  string
	Model    string
}

type LiveTurn struct {
	ThreadID string
	TurnID   string
	Status   LiveTurnStatus
}

type LiveTurnStatus string

const (
	LiveTurnStatusCompleted   LiveTurnStatus = "completed"
	LiveTurnStatusInterrupted LiveTurnStatus = "interrupted"
	LiveTurnStatusFailed      LiveTurnStatus = "failed"
	LiveTurnStatusInProgress  LiveTurnStatus = "inProgress"
)

type LiveEvent struct {
	Kind     LiveEventKind
	ThreadID string
	TurnID   string
	ItemID   string
	Delta    string
	Status   LiveTurnStatus
	Err      error
}

type LiveEventKind string

const (
	LiveEventDelta     LiveEventKind = "delta"
	LiveEventCompleted LiveEventKind = "completed"
	LiveEventError     LiveEventKind = "error"
)

type LiveRuntimeOptions struct {
	CodexBin       string
	Command        []string
	WorkDir        string
	Env            []string
	ClientName     string
	ClientTitle    string
	ClientVersion  string
	Experimental   bool
	ConfigOverride []string
}

func NewLiveRuntime(opts LiveRuntimeOptions) LiveRuntime {
	return NewAppServerRuntime(AppServerRuntimeOptions(opts))
}

func validateLiveStartRequest(req LiveStartRequest) error {
	if strings.TrimSpace(req.WorkDir) == "" {
		return fmt.Errorf("missing codex live work dir")
	}
	return nil
}

func validateLiveAttachRequest(req LiveAttachRequest) error {
	if strings.TrimSpace(req.ThreadID) == "" {
		return fmt.Errorf("missing codex live thread id")
	}
	return nil
}

func validateLiveSendRequest(req LiveSendRequest) error {
	if strings.TrimSpace(req.ThreadID) == "" {
		return fmt.Errorf("missing codex live thread id")
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("missing codex live input text")
	}
	return nil
}

func validateLiveStreamRequest(req LiveStreamRequest) error {
	if strings.TrimSpace(req.ThreadID) == "" {
		return fmt.Errorf("missing codex live thread id")
	}
	if strings.TrimSpace(req.TurnID) == "" {
		return fmt.Errorf("missing codex live turn id")
	}
	return nil
}

func validateLiveStopRequest(req LiveStopRequest) error {
	if strings.TrimSpace(req.ThreadID) == "" {
		return fmt.Errorf("missing codex live thread id")
	}
	if strings.TrimSpace(req.TurnID) == "" {
		return fmt.Errorf("missing codex live turn id")
	}
	return nil
}
