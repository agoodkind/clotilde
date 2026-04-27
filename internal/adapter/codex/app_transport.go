package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type RPCID struct {
	raw string
}

func NewRPCID(id int) RPCID {
	return RPCID{raw: strconv.Itoa(id)}
}

func (id RPCID) IntString() string {
	return id.raw
}

func (id RPCID) Equals(want int) bool {
	return id.raw == strconv.Itoa(want)
}

func (id *RPCID) UnmarshalJSON(raw []byte) error {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		id.raw = ""
		return nil
	}
	if unquoted, err := strconv.Unquote(trimmed); err == nil {
		id.raw = strings.TrimSpace(unquoted)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	id.raw = number.String()
	return nil
}

type RPCMessage struct {
	ID     *RPCID          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type RPCClient interface {
	SendInitialize(id int, params RPCInitializeParams) error
	NotifyInitialized() error
	SendThreadStart(id int, params RPCThreadStartParams) error
	SendTurnStart(id int, params RPCTurnStartParams) error
	SendThreadArchive(id int, params RPCThreadArchiveParams) error
	Next() (RPCMessage, error)
	Close() error
}

type RPCStarter func(context.Context, string, map[string]string) (RPCClient, error)

type AppTransport struct {
	cancel                context.CancelFunc
	rpc                   RPCClient
	threadID              string
	lastCachedInputTokens int
}

func NewAppTransport(bin string, spec SessionSpec, start RPCStarter) (*AppTransport, error) {
	sessCtx, cancel := context.WithCancel(context.Background())
	rpc, err := start(sessCtx, bin, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	t := &AppTransport{cancel: cancel, rpc: rpc}
	cleanup := func(err error) (*AppTransport, error) {
		_ = t.Close()
		return nil, err
	}

	if err := rpc.SendInitialize(1, RPCInitializeParams{
		ClientInfo: RPCClientInfo{
			Name:    "clyde-adapter",
			Title:   "Clyde Adapter",
			Version: "0.1.0",
		},
	}); err != nil {
		return cleanup(err)
	}
	if _, err := waitFor(rpc, 1); err != nil {
		return cleanup(err)
	}
	if err := rpc.NotifyInitialized(); err != nil {
		return cleanup(err)
	}
	if err := rpc.SendThreadStart(2, RPCThreadStartParams{
		Model:                  spec.Model,
		Cwd:                    ".",
		ApprovalPolicy:         AskForApprovalNever,
		Ephemeral:              true,
		ServiceName:            "clyde-codex-session",
		BaseInstructions:       spec.System,
		ExperimentalRawEvents:  false,
		PersistExtendedHistory: false,
	}); err != nil {
		return cleanup(err)
	}
	startResp, err := waitFor(rpc, 2)
	if err != nil {
		return cleanup(err)
	}
	var threadResp RPCThreadStartResponse
	if err := json.Unmarshal(startResp.Result, &threadResp); err != nil {
		return cleanup(err)
	}
	t.threadID = threadResp.Thread.ID
	return t, nil
}

func (t *AppTransport) Close() error {
	if t == nil {
		return nil
	}
	if t.rpc != nil && strings.TrimSpace(t.threadID) != "" {
		_ = t.rpc.SendThreadArchive(9, RPCThreadArchiveParams{ThreadID: t.threadID})
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.rpc != nil {
		return t.rpc.Close()
	}
	return nil
}

func (t *AppTransport) SendTurnStart(id int, params RPCTurnStartParams) error {
	return t.rpc.SendTurnStart(id, params)
}

func (t *AppTransport) Next() (RPCMessage, error) {
	return t.rpc.Next()
}

func (t *AppTransport) ThreadID() string {
	return t.threadID
}

func (t *AppTransport) CachedInputTokens() int {
	return t.lastCachedInputTokens
}

func (t *AppTransport) SetCachedInputTokens(v int) {
	t.lastCachedInputTokens = v
}

func RPCIDEquals(v *RPCID, want int) bool {
	return v != nil && v.Equals(want)
}

func waitFor(rpc RPCClient, id int) (RPCMessage, error) {
	for {
		msg, err := rpc.Next()
		if err != nil {
			return RPCMessage{}, err
		}
		if msg.ID == nil || !RPCIDEquals(msg.ID, id) {
			continue
		}
		if msg.Error != nil {
			return RPCMessage{}, fmt.Errorf("codex rpc %s", strings.TrimSpace(msg.Error.Message))
		}
		return msg, nil
	}
}
