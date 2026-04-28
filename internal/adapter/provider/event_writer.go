package provider

import adapterrender "goodkind.io/clyde/internal/adapter/render"

// EventWriter is the only path a Provider has into the OpenAI wire
// stream. Providers emit normalized render.Event values; the writer
// implementation owns SSE framing, buffering, and flushing. Backend
// packages must not bypass the writer to call into render or
// openai-wire helpers directly.
type EventWriter interface {
	// WriteEvent serializes one normalized event onto the wire. It
	// must be safe to call from a single goroutine; concurrent
	// callers must serialize through their own coordination.
	WriteEvent(ev adapterrender.Event) error
	// Flush forces buffered output to the network. Providers call
	// it at well-defined boundaries (response.created,
	// response.completed) so clients see incremental progress.
	Flush() error
}
