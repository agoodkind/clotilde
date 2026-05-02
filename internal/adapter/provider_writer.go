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
	sse               *adapteropenai.SSEWriter
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
	sse, err := adapteropenai.NewSSEWriter(w)
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
	finishReason := normalizedProviderFinishReason(result)
	finishChunk := adapteropenai.StreamChunk{
		ID:      p.reqID,
		Object:  "chat.completion.chunk",
		Created: p.createdUnix(),
		Model:   p.modelAlias,
		Choices: []adapteropenai.StreamChoice{{
			Index:        0,
			Delta:        adapteropenai.StreamDelta{},
			FinishReason: &finishReason,
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

func (p *providerStreamWriter) writeStreamError(kind, message string) error {
	return p.writeStreamErrorBody(adapteropenai.ErrorBody{Message: message, Type: kind, Code: kind})
}

func (p *providerStreamWriter) writeStreamErrorBody(body adapteropenai.ErrorBody) error {
	if p == nil || p.sse == nil {
		return nil
	}
	if err := p.sse.EmitStreamError(body); err != nil {
		return err
	}
	return p.sse.WriteStreamDone()
}

var _ adapterprovider.EventWriter = (*providerStreamWriter)(nil)

// providerCollectorWriter implements provider.EventWriter for the
// non-streaming response path. It buffers normalized events in memory;
// provider-specific collect reducers assemble final ChatResponses from
// those events after Execute returns.
type providerCollectorWriter struct {
	events []adapterrender.Event
}

func newProviderCollectorWriter() *providerCollectorWriter {
	return &providerCollectorWriter{}
}

func (p *providerCollectorWriter) appendEvent(ev adapterrender.Event) error {
	p.events = append(p.events, ev)
	return nil
}

func (p *providerCollectorWriter) WriteEvent(ev adapterrender.Event) error {
	if p == nil {
		return nil
	}
	return p.appendEvent(ev)
}

func (p *providerCollectorWriter) Flush() error {
	return nil
}

var _ adapterprovider.EventWriter = (*providerCollectorWriter)(nil)

func (p *providerStreamWriter) createdUnix() int64 {
	if p != nil && p.renderer != nil {
		return p.renderer.CreatedUnix()
	}
	return 0
}

func normalizedProviderFinishReason(result adapterprovider.Result) string {
	finishReason := strings.TrimSpace(result.FinishReason)
	if result.ToolCallCount > 0 && finishReason != "length" && finishReason != "content_filter" {
		return "tool_calls"
	}
	if finishReason == "" {
		return defaultProviderFinishReason
	}
	return finishReason
}
