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
// Returns matching messages grouped by relevance.
func Search(ctx context.Context, messages []transcript.Message, query string, cfg config.SearchConfig) ([]Result, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	client := NewClient(cfg)
	chunks := chunkMessages(messages, maxChunkChars)

	// Search all chunks in parallel
	type chunkResult struct {
		idx     int
		result  *Result
		err     error
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

	// Collect matches in order
	var matched []Result
	for _, r := range results {
		if r.err != nil {
			continue // skip failed chunks
		}
		if r.result != nil && len(r.result.Messages) > 0 {
			matched = append(matched, *r.result)
		}
	}

	// Re-rank: ask the LLM which results are actually relevant
	if len(matched) > 1 {
		matched = rerankResults(ctx, client, matched, query)
	}

	return matched, nil
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
