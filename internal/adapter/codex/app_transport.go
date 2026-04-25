package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type RPCMessage struct {
	ID     any             `json:"id,omitempty"`
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
	Send(id int, method string, params any) error
	Notify(method string, params any) error
	Next() (RPCMessage, error)
	Close() error
}

type RPCStarter func(context.Context, string) (RPCClient, error)

type AppTransport struct {
	cancel                context.CancelFunc
	rpc                   RPCClient
	threadID              string
	lastCachedInputTokens int
}

func NewAppTransport(bin string, spec SessionSpec, start RPCStarter) (*AppTransport, error) {
	sessCtx, cancel := context.WithCancel(context.Background())
	rpc, err := start(sessCtx, bin)
	if err != nil {
		cancel()
		return nil, err
	}
	t := &AppTransport{cancel: cancel, rpc: rpc}
	cleanup := func(err error) (*AppTransport, error) {
		_ = t.Close()
		return nil, err
	}

	if err := rpc.Send(1, "initialize", map[string]any{
		"clientInfo": map[string]any{
			"name":    "clyde-adapter",
			"title":   "Clyde Adapter",
			"version": "0.1.0",
		},
	}); err != nil {
		return cleanup(err)
	}
	if _, err := waitFor(rpc, 1); err != nil {
		return cleanup(err)
	}
	if err := rpc.Notify("initialized", map[string]any{}); err != nil {
		return cleanup(err)
	}
	if err := rpc.Send(2, "thread/start", map[string]any{
		"model":                  spec.Model,
		"cwd":                    ".",
		"approvalPolicy":         "never",
		"ephemeral":              true,
		"serviceName":            "clyde-codex-session",
		"baseInstructions":       spec.System,
		"experimentalRawEvents":  false,
		"persistExtendedHistory": false,
	}); err != nil {
		return cleanup(err)
	}
	startResp, err := waitFor(rpc, 2)
	if err != nil {
		return cleanup(err)
	}
	var threadResp struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
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
		_ = t.rpc.Send(9, "thread/archive", map[string]any{"threadId": t.threadID})
	}
	if t.cancel != nil {
		t.cancel()
	}
	if t.rpc != nil {
		return t.rpc.Close()
	}
	return nil
}

func (t *AppTransport) Send(id int, method string, params any) error {
	return t.rpc.Send(id, method, params)
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

func RPCIDEquals(v any, want int) bool {
	switch id := v.(type) {
	case float64:
		return int(id) == want
	case int:
		return id == want
	case string:
		return id == fmt.Sprintf("%d", want)
	default:
		return false
	}
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
