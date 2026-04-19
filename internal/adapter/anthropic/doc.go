// Package anthropic is a minimal HTTP client for the vendor messages API
// used by the adapter's direct-token path.
//
// The client translates OpenAI-shaped chat requests into Messages calls,
// parses streamed SSE events, and surfaces text deltas, tool-use
// lifecycle hints, and final usage. Single text blocks may serialize as a
// string "content" field for prompt-cache-friendly JSON.
//
// File layout: doc.go (this file), types.go (wire types), client.go,
// stream_parse.go, logging.go.
package anthropic
