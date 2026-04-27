package config

import "testing"

func TestSearchLocalResolvedEmbeddingURLFallsBackToURL(t *testing.T) {
	s := SearchLocal{URL: "http://[::1]:1234/"}
	if got := s.ResolvedEmbeddingURL(); got != "http://[::1]:1234" {
		t.Fatalf("got %q", got)
	}
}

func TestSearchLocalResolvedEmbeddingURLUsesEmbeddingURL(t *testing.T) {
	s := SearchLocal{
		URL:          "http://[::1]:1234",
		EmbeddingURL: "http://[::1]:5400/",
	}
	if got := s.ResolvedEmbeddingURL(); got != "http://[::1]:5400" {
		t.Fatalf("got %q", got)
	}
}

func TestSearchLocalResolvedEmbeddingTokenFallsBack(t *testing.T) {
	s := SearchLocal{Token: "chat-token"}
	if got := s.ResolvedEmbeddingToken(); got != "chat-token" {
		t.Fatalf("got %q", got)
	}
}

func TestSearchLocalResolvedEmbeddingTokenOverride(t *testing.T) {
	s := SearchLocal{Token: "chat-token", EmbeddingToken: "emb-token"}
	if got := s.ResolvedEmbeddingToken(); got != "emb-token" {
		t.Fatalf("got %q", got)
	}
}
