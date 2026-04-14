package search

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/transcript"
)

const (
	// maxChunkChars is the target size for each conversation chunk sent to the LLM.
	maxChunkChars = 50_000
	// defaultMaxConcurrent is the default number of parallel LLM requests.
	defaultMaxConcurrent = 4
)

// Result holds matching messages from a search.
type Result struct {
	Messages []transcript.Message
	Summary  string // LLM's description of what matched
}

// Search finds conversation messages matching a query using the configured LLM backend.
// Depth controls how many pipeline layers to use: "quick", "normal" (default), "deep".
func Search(ctx context.Context, messages []transcript.Message, query string, cfg config.SearchConfig) ([]Result, error) {
	return SearchWithDepth(ctx, messages, query, cfg, "normal")
}

// SearchWithDepth finds conversation messages with a configurable search depth.
// "quick": fast model sweep only
// "normal": fast sweep + rerank with medium model
// "deep": fast sweep + rerank + deep analysis with largest model
func SearchWithDepth(ctx context.Context, messages []transcript.Message, query string, cfg config.SearchConfig, depth string) ([]Result, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	pipeline := cfg.Local.ResolvePipeline(depth)
	if len(pipeline) == 0 {
		return nil, fmt.Errorf("no search pipeline configured")
	}

	// Track current model so we only swap when needed
	currentModel := ""

	// Layer 1: sweep with fast model
	sweepLayer := pipeline[0]
	if cfg.Backend == "local" {
		if err := ensureModelLoaded(ctx, sweepLayer.Model); err != nil {
			return nil, fmt.Errorf("failed to load model %s: %w", sweepLayer.Model, err)
		}
		currentModel = sweepLayer.Model
	}
	sweepClient := newClientForModel(cfg, sweepLayer.Model)
	matched := sweepChunks(ctx, sweepClient, messages, query, cfg)

	// Layer 2+: rerank/deep passes with progressively smarter models
	for i := 1; i < len(pipeline); i++ {
		if len(matched) == 0 {
			break
		}
		layer := pipeline[i]

		// Skip rerank if only 1 result (nothing to filter), but still run deep
		if len(matched) <= 1 && layer.Name != "deep" {
			continue
		}

		// Swap model if this layer uses a different one
		if cfg.Backend == "local" && layer.Model != currentModel {
			if err := ensureModelLoaded(ctx, layer.Model); err != nil {
				// Non-fatal: skip this layer if model can't load
				continue
			}
			currentModel = layer.Model
		}

		layerClient := newClientForModel(cfg, layer.Model)

		switch layer.Name {
		case "deep":
			matched = deepAnalysis(ctx, layerClient, matched, query)
		default:
			matched = rerankResults(ctx, layerClient, matched, query)
		}
	}

	return matched, nil
}

// sweepChunks runs the initial parallel chunk search.
func sweepChunks(ctx context.Context, client Client, messages []transcript.Message, query string, cfg config.SearchConfig) []Result {
	chunks := chunkMessages(messages, maxChunkChars)

	type chunkResult struct {
		idx    int
		result *Result
		err    error
	}

	maxConc := cfg.Local.MaxConcurrent
	if maxConc <= 0 {
		maxConc = defaultMaxConcurrent
	}

	results := make([]chunkResult, len(chunks))
	sem := make(chan struct{}, maxConc)
	var wg sync.WaitGroup

	for i, chunk := range chunks {
		wg.Add(1)
		go func(idx int, msgs []transcript.Message) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			res, err := searchChunk(ctx, client, msgs, query)
			results[idx] = chunkResult{idx: idx, result: res, err: err}
		}(i, chunk)
	}
	wg.Wait()

	var matched []Result
	for _, r := range results {
		if r.err != nil {
			continue
		}
		if r.result != nil && len(r.result.Messages) > 0 {
			matched = append(matched, *r.result)
		}
	}
	return matched
}

// deepAnalysis re-evaluates each result with a large model, asking it to
// verify relevance and provide a more detailed summary.
func deepAnalysis(ctx context.Context, client Client, results []Result, query string) []Result {
	var kept []Result
	for _, r := range results {
		// Build the conversation text from matched messages
		var convText strings.Builder
		for _, m := range r.Messages {
			ts := m.Timestamp.Format("2006-01-02 15:04")
			role := "User"
			if m.Role == "assistant" {
				role = "Assistant"
			}
			fmt.Fprintf(&convText, "[%s] %s:\n%s\n\n", ts, role, m.Text)
		}

		prompt := fmt.Sprintf(`You are verifying whether a conversation excerpt is relevant to a search query.

QUERY: %s

CONVERSATION EXCERPT:
%s

Is this conversation excerpt specifically relevant to the query?
If YES: respond with "YES" followed by a detailed 2-3 sentence summary of what was discussed and decided.
If NO: respond with exactly "NO".`, query, convText.String())

		resp, err := client.Complete(ctx, prompt)
		if err != nil {
			kept = append(kept, r) // on error, keep the result
			continue
		}

		resp = strings.TrimSpace(resp)
		if strings.HasPrefix(strings.ToUpper(resp), "NO") {
			continue
		}

		// Extract summary (everything after "YES" or "YES:")
		summary := resp
		if strings.HasPrefix(strings.ToUpper(summary), "YES") {
			summary = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(summary, "YES"), ":"))
		}
		if summary != "" {
			r.Summary = summary
		}
		kept = append(kept, r)
	}
	return kept
}

// rerankResults sends all result summaries to the LLM and asks which are
// genuinely relevant, filtering out false positives from the initial search.
func rerankResults(ctx context.Context, client Client, results []Result, query string) []Result {
	var sb strings.Builder
	for i, r := range results {
		fmt.Fprintf(&sb, "[%d] %s\n", i, r.Summary)
	}

	prompt := fmt.Sprintf(`Given this search query and these result summaries, which results are relevant? Remove only clearly unrelated results.

QUERY: %s

RESULTS:
%s
Return ONLY the numbers of relevant results, one per line. If none are relevant, respond with NONE.`, query, sb.String())

	resp, err := client.Complete(ctx, prompt)
	if err != nil {
		return results // on error, return unfiltered
	}

	resp = strings.TrimSpace(resp)
	if resp == "NONE" || resp == "none" {
		return nil
	}

	var filtered []Result
	for _, line := range strings.Split(resp, "\n") {
		line = strings.TrimSpace(line)
		// Parse bare number or [N] format
		var idx int
		if _, err := fmt.Sscanf(line, "[%d]", &idx); err != nil {
			_, _ = fmt.Sscanf(line, "%d", &idx)
		}
		if idx >= 0 && idx < len(results) {
			filtered = append(filtered, results[idx])
		}
	}
	if len(filtered) == 0 {
		return results // parsing failed, return unfiltered
	}
	return filtered
}

// searchChunk asks the LLM whether a conversation chunk discusses the query.
// If yes, returns the relevant messages. If no, returns nil.
func searchChunk(ctx context.Context, client Client, messages []transcript.Message, query string) (*Result, error) {
	// Build the conversation text for this chunk
	var convText strings.Builder
	for i, m := range messages {
		ts := m.Timestamp.Format("2006-01-02 15:04")
		role := "User"
		if m.Role == "assistant" {
			role = "Assistant"
		}
		fmt.Fprintf(&convText, "[MSG %d][%s] %s:\n%s\n\n", i, ts, role, m.Text)
	}

	prompt := fmt.Sprintf(`You are searching a conversation transcript for a specific topic.

QUERY: %s

CONVERSATION:
%s

INSTRUCTIONS:
- Match if the conversation substantively discusses the query topic.
- Do NOT match on passing mentions or vague keyword overlap.
- If this conversation segment discusses the query topic, respond with the message numbers (MSG N) that are relevant, one per line:
MSG 0
MSG 3
MSG 4
- After the message numbers, add a blank line and a one-sentence summary.
- If this conversation does NOT discuss the query topic, respond with exactly: NO

Respond ONLY with message numbers + summary, or NO. Nothing else.`, query, convText.String())

	resp, err := client.Complete(ctx, prompt)
	if err != nil {
		return nil, err
	}

	resp = strings.TrimSpace(resp)
	if resp == "NO" || resp == "no" || resp == "No" {
		return nil, nil
	}

	// Parse message numbers from response
	lines := strings.Split(resp, "\n")
	var matchedMessages []transcript.Message
	var summary string
	pastNumbers := false

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			pastNumbers = true
			continue
		}
		if !pastNumbers {
			var msgIdx int
			if _, err := fmt.Sscanf(line, "MSG %d", &msgIdx); err == nil {
				if msgIdx >= 0 && msgIdx < len(messages) {
					matchedMessages = append(matchedMessages, messages[msgIdx])
				}
			}
		} else {
			summary = line
		}
	}

	if len(matchedMessages) == 0 {
		return nil, nil
	}

	return &Result{
		Messages: matchedMessages,
		Summary:  summary,
	}, nil
}

// chunkMessages splits messages into chunks of approximately maxChars each,
// splitting on message boundaries (never mid-message).
func chunkMessages(messages []transcript.Message, maxChars int) [][]transcript.Message {
	var chunks [][]transcript.Message
	var current []transcript.Message
	currentLen := 0

	for _, m := range messages {
		msgLen := len(m.Text) + 50 // overhead for timestamp/role
		if currentLen+msgLen > maxChars && len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			currentLen = 0
		}
		current = append(current, m)
		currentLen += msgLen
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
