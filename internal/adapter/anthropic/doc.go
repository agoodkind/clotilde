// Package anthropic is a minimal /v1/messages client tailored to the
// clyde adapter. It speaks the Anthropic native API directly (not the
// OpenAI-shaped surface) so the adapter can authenticate with the
// user's Claude.ai OAuth bearer token and bill against their
// subscription instead of going through the metered API key path.
//
// The client translates OpenAI-style chat requests into Messages calls,
// parses streamed SSE events, and surfaces text deltas, tool-use
// lifecycle hints, and final usage to the caller. Prompt caching stays
// friendly for the common single text block via string-shaped JSON
// content when marshaling a lone text block.
//
// Wire format references (from claude-code-2.1.88 sourcemap):
//   - POST https://REDACTED-UPSTREAM/v1/messages
//   - headers: Authorization: Bearer <oauth>, anthropic-beta:
//     REDACTED-OAUTH-BETA, anthropic-version: 2023-06-01,
//     x-app: cli, content-type: application/json
//   - body: { model, system, messages, max_tokens, stream, thinking? }
//   - SSE event types relayed back: message_start, content_block_start,
//     content_block_delta, content_block_stop, message_delta, message_stop
//
// File layout: doc.go (this file), types.go (wire types), client.go (HTTP
// and StreamEvents), stream_parse.go (SSE decode), logging.go (response
// telemetry and JSONL mirror).
package anthropic
