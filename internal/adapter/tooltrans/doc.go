// Package tooltrans contains cross-backend sentinel cleanup helpers.
//
// Backend-specific request mapping, SSE parsing, and provider semantics should
// live in the owning backend package. Anthropic translation lives in
// internal/adapter/anthropic/backend; Codex translation lives in
// internal/adapter/codex. Shared OpenAI wire types live in
// internal/adapter/openai, and shared output rendering lives in
// internal/adapter/render.
package tooltrans
