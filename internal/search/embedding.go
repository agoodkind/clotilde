package search

import (
	"context"
	"log/slog"
	"math"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/fgrehm/clotilde/internal/config"
	"github.com/fgrehm/clotilde/internal/transcript"
)

const (
	defaultEmbeddingModel      = "text-embedding-nomic-embed-text-v1.5"
	defaultSimilarityThreshold = 0.5
)

// embeddingFilter uses a local embedding model to pre-filter chunks,
// skipping those with low cosine similarity to the query.
type embeddingFilter struct {
	client    *openai.Client
	model     string
	threshold float64
}

func newEmbeddingFilter(cfg config.SearchLocal) *embeddingFilter {
	url := cfg.URL
	if url == "" {
		url = "http://localhost:1234"
	}

	opts := []option.RequestOption{
		option.WithBaseURL(url + "/v1"),
	}
	if cfg.Token != "" {
		opts = append(opts, option.WithAPIKey(cfg.Token))
	} else {
		opts = append(opts, option.WithAPIKey("not-needed"))
	}

	threshold := cfg.EmbeddingThreshold
	if threshold <= 0 {
		threshold = defaultSimilarityThreshold
	}

	c := openai.NewClient(opts...)
	return &embeddingFilter{client: &c, model: defaultEmbeddingModel, threshold: threshold}
}

// filterChunks embeds the query and each chunk, returning only chunks whose
// cosine similarity to the query exceeds the threshold.
func (e *embeddingFilter) filterChunks(ctx context.Context, query string, chunks [][]transcript.Message) ([][]transcript.Message, error) {
	log := slog.Default()
	start := time.Now()

	// Build text for each chunk
	chunkTexts := make([]string, len(chunks))
	for i, chunk := range chunks {
		var text string
		for _, m := range chunk {
			text += m.Text + "\n"
			if len(text) > 2000 {
				text = text[:2000]
				break
			}
		}
		chunkTexts[i] = text
	}

	// Embed query
	queryEmbStart := time.Now()
	queryEmb, err := e.embed(ctx, []string{query})
	if err != nil {
		log.Warn("embedding query failed, skipping pre-filter", "err", err)
		return chunks, nil // fall back to no filtering
	}
	log.Debug("embedding: query embedded", "duration", time.Since(queryEmbStart).Round(time.Millisecond))

	// Embed all chunks in one batch
	chunksEmbStart := time.Now()
	chunkEmbs, err := e.embed(ctx, chunkTexts)
	if err != nil {
		log.Warn("embedding chunks failed, skipping pre-filter", "err", err)
		return chunks, nil
	}
	log.Debug("embedding: chunks embedded", "chunks", len(chunkTexts), "duration", time.Since(chunksEmbStart).Round(time.Millisecond))

	if len(queryEmb) == 0 || len(chunkEmbs) != len(chunks) {
		return chunks, nil
	}

	// Filter by cosine similarity
	queryVec := queryEmb[0]
	var filtered [][]transcript.Message
	for i, chunkVec := range chunkEmbs {
		sim := cosineSimilarity(queryVec, chunkVec)
		if sim >= e.threshold {
			filtered = append(filtered, chunks[i])
		}
	}

	log.Info("embedding pre-filter complete",
		"model", e.model,
		"total_chunks", len(chunks),
		"passed", len(filtered),
		"filtered_out", len(chunks)-len(filtered),
		"threshold", e.threshold,
		"query_embed_duration", time.Since(queryEmbStart).Round(time.Millisecond),
		"chunks_embed_duration", time.Since(chunksEmbStart).Round(time.Millisecond),
		"total_duration", time.Since(start).Round(time.Millisecond),
	)

	if len(filtered) == 0 {
		// If nothing passed, return all chunks (threshold might be too high)
		log.Warn("embedding filter removed all chunks, falling back to unfiltered")
		return chunks, nil
	}

	return filtered, nil
}

// embed returns embeddings for the given texts.
func (e *embeddingFilter) embed(ctx context.Context, texts []string) ([][]float64, error) {
	resp, err := e.client.Embeddings.New(ctx, openai.EmbeddingNewParams{
		Model: e.model,
		Input: openai.EmbeddingNewParamsInputUnion{
			OfArrayOfStrings: texts,
		},
	})
	if err != nil {
		return nil, err
	}

	result := make([][]float64, len(resp.Data))
	for _, d := range resp.Data {
		result[d.Index] = d.Embedding
	}
	return result, nil
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}
