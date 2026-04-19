// Package adapter implements the OpenAI compatible HTTP surface folded into
// the clyde daemon. A single launchd entry boots the gRPC daemon plus this
// adapter so clients pointing at a local URL can drive the Claude Max
// subscription through claude -p.
//
// Subpackages: anthropic (/v1/messages client), oauth (token lifecycle),
// fallback (CLI subprocess), tooltrans (OpenAI↔Anthropic translation),
// finishreason (stop_reason mapping).
//
// Top-level Go files (by concern): doc.go (this overview), server.go and
// server_routes.go (listener and routes), server_preflight.go (capability gates),
// server_dispatch.go (handleChat and backends), server_streaming.go (SSE helper),
// server_response.go (OAuth chunk merge), server_shunt.go (HTTP proxy),
// server_json_retry.go (structured output policy), contextlog.go (detached
// context), stream_chunk_convert.go, stream.go (legacy stream-json), oauth_handler.go,
// fallback_handler.go, models.go (registry), jsonschema.go, runner.go, openai.go.
package adapter
