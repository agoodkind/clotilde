package provider

import (
	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
	adapterrender "goodkind.io/clyde/internal/adapter/render"
)

// EventWriter is the only path a Provider has into the OpenAI wire
// stream. Providers may emit either:
//
//   - typed StreamChunks via WriteStreamChunk, when the provider's
//     parser already produces OpenAI-shaped chunks (today: codex);
//   - typed normalized events via WriteEvent, when the provider's
//     parser produces render.Event values (target shape; populated
//     once Plan 6 lands).
//
// Implementations of EventWriter own SSE framing, buffering, and
// flushing. Both methods drive the same outbound wire stream;
// callers may interleave them safely so a provider can mix raw
// chunks with normalized events while the migration is in flight.
type EventWriter interface {
	// WriteStreamChunk serializes one OpenAI-shaped stream chunk
	// onto the wire. Codex uses this path today because its parser
	// emits StreamChunks directly. Providers that produce
	// normalized events should prefer WriteEvent.
	WriteStreamChunk(chunk adapteropenai.StreamChunk) error
	// WriteEvent serializes one normalized event onto the wire.
	// Implementations may convert the event into a StreamChunk
	// internally before writing.
	WriteEvent(ev adapterrender.Event) error
	// Flush forces buffered output to the network. Providers call
	// it at well-defined boundaries (response.created,
	// response.completed) so clients see incremental progress.
	Flush() error
}
