package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
)

type stdioRPCClient struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
}

func StartRPC(ctx context.Context, bin string) (RPCClient, error) {
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
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &stdioRPCClient{
		cmd:    cmd,
		stdin:  stdin,
		stdout: bufio.NewReader(stdout),
	}, nil
}

func (c *stdioRPCClient) Send(id int, method string, params any) error {
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = io.WriteString(c.stdin, string(raw)+"\n")
	return err
}

func (c *stdioRPCClient) Notify(method string, params any) error {
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
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
