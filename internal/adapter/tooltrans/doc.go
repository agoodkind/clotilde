// Package tooltrans is a small compatibility layer for shared adapter stream
// and OpenAI wire aliases.
//
// Backend-specific request mapping, SSE parsing, and provider semantics should
// live in the owning backend package. Anthropic translation lives in
// internal/adapter/anthropic/backend; Codex translation lives in
// internal/adapter/codex. This package remains for shared render aliases,
// OpenAI wire aliases, and cross-backend sentinel cleanup helpers.
package tooltrans
