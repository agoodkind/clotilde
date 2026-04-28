package adapter

import (
	"net/http"

	adaptercodex "goodkind.io/clyde/internal/adapter/codex"
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

// providerStreamWriter implements provider.EventWriter on top of the
// existing SSE writer the adapter already constructs for streaming
// responses. WriteStreamChunk forwards directly to the underlying
// codex.SSEWriter; WriteEvent will be filled in by Plan 6 once the
// render pipeline emits normalized events. Today it returns nil to
// keep the typed Provider interface satisfied without changing wire
// behavior.
type providerStreamWriter struct {
	sse               adaptercodex.SSEWriter
	systemFingerprint string
	headersWritten    bool
	flusher           http.Flusher
}

func newProviderStreamWriter(s *Server, w http.ResponseWriter) (*providerStreamWriter, error) {
	sse, err := s.NewSSEWriter(w)
	if err != nil {
		return nil, err
	}
	flusher, _ := w.(http.Flusher)
	return &providerStreamWriter{
		sse:               sse,
		systemFingerprint: s.SystemFingerprint(),
		flusher:           flusher,
	}, nil
}

func (p *providerStreamWriter) WriteStreamChunk(chunk adapteropenai.StreamChunk) error {
	if !p.headersWritten {
		p.sse.WriteSSEHeaders()
		p.headersWritten = true
	}
	return p.sse.EmitStreamChunk(p.systemFingerprint, chunk)
}

// WriteEvent is the future-state path for normalized render events.
// Plan 6 (render finalization) replaces the StreamChunk emit path
// with normalized events; until then this method is a no-op so the
// Provider interface stays satisfied without losing fidelity.
func (p *providerStreamWriter) WriteEvent(_ adapterrender.Event) error {
	return nil
}

func (p *providerStreamWriter) Flush() error {
	if p.flusher != nil {
		p.flusher.Flush()
	}
	return nil
}

// finalizeStream writes the SSE [DONE] sentinel after the provider's
// Execute returns successfully. It is the streaming counterpart of
// the chat.completion JSON envelope written by the non-streaming
// path.
func (p *providerStreamWriter) finalizeStream() error {
	return p.sse.WriteStreamDone()
}

var _ adapterprovider.EventWriter = (*providerStreamWriter)(nil)

// providerCollectorWriter implements provider.EventWriter for the
// non-streaming response path. It buffers every chunk in memory; the
// dispatcher then merges the buffered chunks into the final
// ChatResponse JSON envelope.
type providerCollectorWriter struct {
	chunks []adapteropenai.StreamChunk
}

func newProviderCollectorWriter() *providerCollectorWriter {
	return &providerCollectorWriter{}
}

func (p *providerCollectorWriter) WriteStreamChunk(chunk adapteropenai.StreamChunk) error {
	p.chunks = append(p.chunks, chunk)
	return nil
}

func (p *providerCollectorWriter) WriteEvent(_ adapterrender.Event) error {
	return nil
}

func (p *providerCollectorWriter) Flush() error {
	return nil
}

var _ adapterprovider.EventWriter = (*providerCollectorWriter)(nil)
