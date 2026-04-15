package cmd

import (
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/search"
	"github.com/fgrehm/clotilde/internal/transcript"
)

// defaultBenchModels is the set of embedding models to compare by default.
// These are the LM Studio model identifiers (as shown by lms ls).
var defaultBenchModels = []string{
	"text-embedding-nomic-embed-text-v1.5",
	"text-embedding-mxbai-embed-large-v1",
	"text-embedding-bge-large-en-v1.5",
	"text-embedding-bge-m3",
	"text-embedding-snowflake-arctic-embed-l",
}

func newBenchEmbedCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bench-embed <session> <query>",
		Short: "Benchmark embedding models against a real session and query",
		Long: `bench-embed loads each embedding model via lms, embeds the session's
transcript chunks and the query, then prints score distributions and timing.

A good model shows a wide gap between irrelevant chunks (low scores) and
relevant ones (high scores). A weak model bunches all scores in the middle,
making threshold filtering unreliable.

Example:
  clotilde bench-embed mwan-opnsense "one time reboot to stabilize routes"`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: sessionNameCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			sessionArg, query := args[0], args[1]

			models, _ := cmd.Flags().GetStringSlice("models")
			if len(models) == 0 {
				models = defaultBenchModels
			}
			thresholds, _ := cmd.Flags().GetFloat64Slice("thresholds")
			if len(thresholds) == 0 {
				thresholds = []float64{0.4, 0.5, 0.6, 0.7}
			}

			store, err := globalStore()
			if err != nil {
				return err
			}
			sess, err := resolveSessionForResume(cmd, store, sessionArg)
			if err != nil {
				return err
			}
			if sess == nil {
				return fmt.Errorf("session '%s' not found", sessionArg)
			}

			messages, loadErr := loadSessionMessages(sess)
			if loadErr != nil {
				return fmt.Errorf("failed to load conversation: %w", loadErr)
			}
			if len(messages) == 0 {
				return fmt.Errorf("no messages found in session")
			}

			cfg, _ := config.LoadGlobalOrDefault()
			baseURL := cfg.Search.Local.URL
			if baseURL == "" {
				baseURL = "http://localhost:1234"
			}
			token := cfg.Search.Local.Token

			chunkSize := cfg.Search.Local.ChunkSize
			if chunkSize <= 0 {
				chunkSize = 4000
			}
			chunks := chunkMessages(messages, chunkSize)

			// Build chunk texts once (shared across all models)
			chunkTexts := make([]string, len(chunks))
			for i, chunk := range chunks {
				var sb strings.Builder
				for _, m := range chunk {
					sb.WriteString(m.Text)
					sb.WriteString("\n")
					if sb.Len() > 2000 {
						break
					}
				}
				chunkTexts[i] = sb.String()
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Benchmarking embedding models\n")
			fmt.Fprintf(out, "Session: %s (%d messages, %d chunks)\n", sess.Name, len(messages), len(chunks))
			fmt.Fprintf(out, "Query:   %s\n\n", query)

			lmsPath, _ := exec.LookPath("lms")

			results := make([]benchResult, 0, len(models))
			for _, model := range models {
				result := runModelBenchmark(out, model, query, chunkTexts, baseURL, token, thresholds, lmsPath)
				results = append(results, result)
			}

			// Summary comparison table
			fmt.Fprintf(out, "\n%s\n", strings.Repeat("=", 70))
			fmt.Fprintf(out, "SUMMARY\n")
			fmt.Fprintf(out, "%s\n", strings.Repeat("=", 70))
			fmt.Fprintf(out, "%-42s  %6s  %6s  %6s  %6s  %8s\n",
				"Model", "Median", "p75", "p90", "Max", "EmbedTime")
			fmt.Fprintf(out, "%s\n", strings.Repeat("-", 70))
			for _, r := range results {
				if r.err != nil {
					fmt.Fprintf(out, "%-42s  ERROR: %v\n", r.model, r.err)
					continue
				}
				fmt.Fprintf(out, "%-42s  %6.3f  %6.3f  %6.3f  %6.3f  %8s\n",
					r.model, r.median, r.p75, r.p90, r.max, r.embedDuration.Round(time.Millisecond))
			}

			return nil
		},
	}

	cmd.Flags().StringSlice("models", nil, "Models to test (default: all known local embedding models)")
	cmd.Flags().Float64Slice("thresholds", nil, "Thresholds to report kept-chunk count for (default: 0.4 0.5 0.6 0.7)")
	return cmd
}

type benchResult struct {
	model         string
	scores        []float64
	embedDuration time.Duration
	median        float64
	p75           float64
	p90           float64
	max           float64
	err           error
}

func runModelBenchmark(
	out io.Writer,
	model, query string,
	chunkTexts []string,
	baseURL, token string,
	thresholds []float64,
	lmsPath string,
) benchResult {
	sep := strings.Repeat("-", 60)
	fmt.Fprintf(out, "%s\n", sep)
	fmt.Fprintf(out, "Model: %s\n", model)

	ctx := context.Background()

	// Load model via lms if available, unload after benchmark
	if lmsPath != "" {
		fmt.Fprintf(out, "  Loading...")
		loadCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		lmsCmd := exec.CommandContext(loadCtx, lmsPath, "load", model, "-y")
		loadStart := time.Now()
		loadErr := lmsCmd.Run()
		cancel()
		loadDuration := time.Since(loadStart)
		if loadErr != nil {
			fmt.Fprintf(out, " FAILED (%v)\n\n", loadErr)
			return benchResult{model: model, err: loadErr}
		}
		fmt.Fprintf(out, " done (%s)\n", loadDuration.Round(time.Millisecond))
		defer func() {
			unloadCtx, unloadCancel := context.WithTimeout(context.Background(), 30*time.Second)
			exec.CommandContext(unloadCtx, lmsPath, "unload", model).Run() //nolint:errcheck
			unloadCancel()
		}()
	}

	// Embed query + all chunks in one batch
	allTexts := append([]string{query}, chunkTexts...)
	embedStart := time.Now()
	embeddings, embedErr := search.EmbedTexts(ctx, baseURL, token, model, allTexts)
	embedDuration := time.Since(embedStart)

	if embedErr != nil {
		fmt.Fprintf(out, "  Embed: FAILED (%v)\n\n", embedErr)
		return benchResult{model: model, err: embedErr}
	}
	fmt.Fprintf(out, "  Embed: %s (%d vectors)\n", embedDuration.Round(time.Millisecond), len(embeddings))

	if len(embeddings) < 2 {
		err := fmt.Errorf("got %d embeddings, expected %d", len(embeddings), len(allTexts))
		fmt.Fprintf(out, "  Error: %v\n\n", err)
		return benchResult{model: model, err: err}
	}

	queryVec := embeddings[0]
	scores := make([]float64, len(chunkTexts))
	for i, chunkVec := range embeddings[1:] {
		scores[i] = search.CosineSimilarity(queryVec, chunkVec)
	}

	// Percentile stats
	sorted := make([]float64, len(scores))
	copy(sorted, scores)
	sort.Float64s(sorted)

	minScore := sorted[0]
	maxScore := sorted[len(sorted)-1]
	median := percentile(sorted, 50)
	p25 := percentile(sorted, 25)
	p75 := percentile(sorted, 75)
	p90 := percentile(sorted, 90)

	fmt.Fprintf(out, "  Scores:  min=%.3f  p25=%.3f  median=%.3f  p75=%.3f  p90=%.3f  max=%.3f\n",
		minScore, p25, median, p75, p90, maxScore)

	// Threshold kept counts
	var threshParts []string
	for _, t := range thresholds {
		kept := 0
		for _, s := range scores {
			if s >= t {
				kept++
			}
		}
		threshParts = append(threshParts, fmt.Sprintf("%.1f: %d/%d (%.0f%%)",
			t, kept, len(scores), 100*float64(kept)/float64(len(scores))))
	}
	fmt.Fprintf(out, "  Kept:    %s\n", strings.Join(threshParts, "  |  "))

	// Histogram
	fmt.Fprintf(out, "  Distribution:\n")
	printHistogram(out, scores)

	fmt.Fprintln(out)
	return benchResult{
		model:         model,
		scores:        scores,
		embedDuration: embedDuration,
		median:        median,
		p75:           p75,
		p90:           p90,
		max:           maxScore,
	}
}

// printHistogram prints a horizontal bar chart of score buckets from 0.0 to 1.0.
func printHistogram(out io.Writer, scores []float64) {
	const buckets = 10
	const bucketWidth = 1.0 / buckets
	counts := make([]int, buckets)
	for _, s := range scores {
		idx := int(s / bucketWidth)
		if idx >= buckets {
			idx = buckets - 1
		}
		if idx < 0 {
			idx = 0
		}
		counts[idx]++
	}
	maxCount := 0
	for _, c := range counts {
		if c > maxCount {
			maxCount = c
		}
	}
	const barMax = 30
	for i, c := range counts {
		lo := float64(i) * bucketWidth
		hi := lo + bucketWidth
		barLen := 0
		if maxCount > 0 {
			barLen = int(math.Round(float64(c) / float64(maxCount) * float64(barMax)))
		}
		bar := strings.Repeat("█", barLen)
		fmt.Fprintf(out, "    %.1f-%.1f  %-30s %d\n", lo, hi, bar, c)
	}
}

// percentile returns the p-th percentile of a sorted slice.
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p / 100) * float64(len(sorted)-1)
	lo := int(idx)
	hi := lo + 1
	if hi >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// chunkMessages splits messages into chunks where each chunk stays under chunkSize characters.
func chunkMessages(messages []transcript.Message, chunkSize int) [][]transcript.Message {
	if chunkSize <= 0 {
		chunkSize = 4000
	}
	var chunks [][]transcript.Message
	var current []transcript.Message
	currentLen := 0
	for _, m := range messages {
		textLen := len(m.Text)
		if currentLen+textLen > chunkSize && len(current) > 0 {
			chunks = append(chunks, current)
			current = nil
			currentLen = 0
		}
		current = append(current, m)
		currentLen += textLen
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}
