package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
)

type stdioRPCClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

type rpcRequestParams interface {
	rpcMethod() string
}

type rpcRequestEnvelope struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int              `json:"id"`
	Method  string           `json:"method"`
	Params  rpcRequestParams `json:"params"`
}

type rpcNotificationEnvelope struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
}

func StartRPC(ctx context.Context, bin string, env map[string]string) (RPCClient, error) {
	cmd := exec.CommandContext(ctx, bin, "app-server", "--listen", "stdio://")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = io.Discard
	if len(env) > 0 {
		cmd.Env = os.Environ()
		for key, value := range env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioRPCClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
}

func (c *stdioRPCClient) SendInitialize(id int, params RPCInitializeParams) error {
	return c.send(id, params)
}

func (c *stdioRPCClient) NotifyInitialized() error {
	raw, err := json.Marshal(rpcNotificationEnvelope{
		JSONRPC: "2.0",
		Method:  "initialized",
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.stdin, string(raw)+"\n")
	return err
}

func (c *stdioRPCClient) SendThreadStart(id int, params RPCThreadStartParams) error {
	return c.send(id, params)
}

func (c *stdioRPCClient) SendTurnStart(id int, params RPCTurnStartParams) error {
	return c.send(id, params)
}

func (c *stdioRPCClient) SendThreadArchive(id int, params RPCThreadArchiveParams) error {
	return c.send(id, params)
}

func (c *stdioRPCClient) send(id int, params rpcRequestParams) error {
	raw, err := json.Marshal(rpcRequestEnvelope{
		JSONRPC: "2.0",
		ID:      id,
		Method:  params.rpcMethod(),
		Params:  params,
	})
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.stdin, string(raw)+"\n")
	return err
}

func (c *stdioRPCClient) Next() (RPCMessage, error) {
	line, err := c.stdout.ReadString('\n')
	if err != nil {
		return RPCMessage{}, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return RPCMessage{}, io.EOF
	}
	var msg RPCMessage
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		return RPCMessage{}, err
	}
	return msg, nil
}

func (c *stdioRPCClient) Close() error {
	if c == nil {
		return nil
	}
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
	if c.stdout != nil {
		_, _ = io.Copy(io.Discard, c.stdout)
	}
	return nil
}
