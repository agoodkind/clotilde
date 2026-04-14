// Package search provides LLM-powered semantic search across conversation transcripts.
package search

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	"github.com/fgrehm/clotilde/internal/config"
)

// Client abstracts the LLM backend for conversation search.
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// NewClient creates a search client from config.
func NewClient(cfg config.SearchConfig) Client {
	switch cfg.Backend {
	case "local":
		return newLocalClient(cfg.Local)
	default:
		return newClaudeClient(cfg.Claude)
	}
}

// localClient uses an OpenAI-compatible endpoint (LM Studio, Ollama, etc.)
type localClient struct {
	client *openai.Client
	model  string
	cfg    config.SearchLocal
}

func newLocalClient(cfg config.SearchLocal) *localClient {
	url := cfg.URL
	if url == "" {
		url = "http://localhost:1234"
	}
	model := cfg.Model
	if model == "" {
		model = "qwen2.5-coder-32b"
	}

	opts := []option.RequestOption{
		option.WithBaseURL(url + "/v1"),
	}
	if cfg.Token != "" {
		opts = append(opts, option.WithAPIKey(cfg.Token))
	} else {
		opts = append(opts, option.WithAPIKey("not-needed"))
	}

	c := openai.NewClient(opts...)
	return &localClient{
		client: &c,
		model:  model,
		cfg:    cfg,
	}
}

func (c *localClient) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		Temperature:      param.NewOpt(c.cfg.Temperature),
		TopP:             param.NewOpt(c.cfg.TopP),
		FrequencyPenalty: param.NewOpt(c.cfg.FrequencyPenalty),
	})
	if err != nil {
		return "", fmt.Errorf("local LLM request failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("local LLM returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
}

// claudeClient shells out to `claude -p` for search.
type claudeClient struct {
	model string
}

func newClaudeClient(cfg config.SearchClaude) *claudeClient {
	model := cfg.Model
	if model == "" {
		model = "haiku"
	}
	return &claudeClient{model: model}
}

func (c *claudeClient) Complete(ctx context.Context, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "claude", "-p", "--model", c.model, prompt)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("claude -p failed: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}
