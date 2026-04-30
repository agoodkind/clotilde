package adapter

import (
	"net/http"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterprovider "goodkind.io/clyde/internal/adapter/provider"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

const defaultProviderFinishReason = "stop"

// providerStreamWriter implements provider.EventWriter on top of the
// shared OpenAI SSE writer. Providers emit normalized render events;
// the adapter renders them into streamed OpenAI chunks privately.
type providerStreamWriter struct {
	sse               *sseWriter
	systemFingerprint string
	headersWritten    bool
	flusher           http.Flusher
	renderer          *adapterrender.EventRenderer
	reqID             string
	modelAlias        string
}

func newProviderStreamWriter(
	s *Server,
	w http.ResponseWriter,
	reqID string,
	modelAlias string,
	backend string,
) (*providerStreamWriter, error) {
	sse, err := newSSEWriter(w)
	if err != nil {
		return nil, err
	}
	flusher, _ := w.(http.Flusher)
	return &providerStreamWriter{
		sse:               sse,
		systemFingerprint: systemFingerprint,
		flusher:           flusher,
		renderer:          adapterrender.NewEventRenderer(reqID, modelAlias, backend, s.log),
		reqID:             reqID,
		modelAlias:        modelAlias,
	}, nil
}

func (p *providerStreamWriter) writeRenderedChunk(chunk adapteropenai.StreamChunk) error {
	if !p.headersWritten {
		p.sse.WriteSSEHeaders()
		p.headersWritten = true
	}
	return p.sse.EmitStreamChunk(p.systemFingerprint, chunk)
}

func (p *providerStreamWriter) WriteEvent(ev adapterrender.Event) error {
	if p == nil || p.renderer == nil {
		return nil
	}
	for _, chunk := range p.renderer.HandleEvent(ev) {
		if err := p.writeRenderedChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (p *providerStreamWriter) Flush() error {
	if p != nil && p.renderer != nil {
		p.renderer.Flush()
	}
	if p.flusher != nil {
		p.flusher.Flush()
	}
	return nil
}

func (p *providerStreamWriter) finalizeStream(result adapterprovider.Result, includeUsage bool) error {
	if err := p.Flush(); err != nil {
		return err
	}
	finishReason := normalizedProviderFinishReason(result.FinishReason)
	finishChunk := adapteropenai.StreamChunk{
		ID:      p.reqID,
		Object:  "chat.completion.chunk",
		Created: p.createdUnix(),
		Model:   p.modelAlias,
		Choices: []adapteropenai.StreamChoice{{
			Index:        0,
			Delta:        adapteropenai.StreamDelta{},
			FinishReason: stringPtrLocal(finishReason),
		}},
	}
	if includeUsage {
		usage := result.Usage
		finishChunk.Usage = &usage
	}
	if err := p.writeRenderedChunk(finishChunk); err != nil {
		return err
	}
	if includeUsage {
		usage := result.Usage
		if err := p.writeRenderedChunk(adapteropenai.StreamChunk{
			ID:      p.reqID,
			Object:  "chat.completion.chunk",
			Created: p.createdUnix(),
			Model:   p.modelAlias,
			Choices: []adapteropenai.StreamChoice{},
			Usage:   &usage,
		}); err != nil {
			return err
		}
	}
	return p.sse.WriteStreamDone()
}

var _ adapterprovider.EventWriter = (*providerStreamWriter)(nil)

// providerCollectorWriter implements provider.EventWriter for the
// non-streaming response path. It buffers adapter-rendered chunks in
// memory because the collect-mode reducers still merge chunk-shaped
// state into final ChatResponses.
type providerCollectorWriter struct {
	chunks   []adapteropenai.StreamChunk
	renderer *adapterrender.EventRenderer
}

func newProviderCollectorWriter(reqID string, modelAlias string, backend string) *providerCollectorWriter {
	return &providerCollectorWriter{
		renderer: adapterrender.NewEventRenderer(reqID, modelAlias, backend, nil),
	}
}

func (p *providerCollectorWriter) appendRenderedChunk(chunk adapteropenai.StreamChunk) error {
	p.chunks = append(p.chunks, chunk)
	return nil
}

func (p *providerCollectorWriter) WriteEvent(ev adapterrender.Event) error {
	if p == nil || p.renderer == nil {
		return nil
	}
	for _, chunk := range p.renderer.HandleEvent(ev) {
		if err := p.appendRenderedChunk(chunk); err != nil {
			return err
		}
	}
	return nil
}

func (p *providerCollectorWriter) Flush() error {
	if p != nil && p.renderer != nil {
		p.renderer.Flush()
	}
	return nil
}

var _ adapterprovider.EventWriter = (*providerCollectorWriter)(nil)

func (p *providerStreamWriter) createdUnix() int64 {
	if p != nil && p.renderer != nil {
		return p.renderer.CreatedUnix()
	}
	return 0
}

func normalizedProviderFinishReason(finishReason string) string {
	finishReason = strings.TrimSpace(finishReason)
	if finishReason == "" {
		return defaultProviderFinishReason
	}
	return finishReason
}
