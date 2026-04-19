// Package oauth reads, caches, and refreshes the Claude Code OAuth
// tokens that the official `claude` CLI stores in the macOS keychain
// (or in ~/.claude/.credentials.json as a fallback).
//
// The adapter uses these tokens to call Anthropic's /v1/messages API
// directly with Bearer auth, so requests bill against the user's
// Claude.ai subscription bucket (Pro/Max/Team/Enterprise) instead of
// the metered API. This mirrors how the `claude` CLI itself
// authenticates when isClaudeAISubscriber() is true.
//
// Behavior verified against the claude-code 2.1.88 sourcemap:
//   - tokens live under keychain service "REDACTED-KEYCHAIN"
//     and a file at $CLAUDE_CONFIG_DIR/.credentials.json
//   - the JSON document has a top level "claudeAiOauth" key with
//     accessToken, refreshToken, expiresAt (ms), scopes, etc.
//   - refresh is POST https://REDACTED-OAUTH-HOST/v1/oauth/token
//     with body { grant_type, refresh_token, client_id, scope }
//   - the public CLIENT_ID is REDACTED-CLIENT-ID
//   - inference calls send Authorization: Bearer + anthropic-beta:
//     REDACTED-OAUTH-BETA + anthropic-version: 2023-06-01
//
// Cross process safety mirrors claude's own implementation: a file
// lock under $CLAUDE_CONFIG_DIR coordinates refresh and the on disk
// credentials file's mtime is sampled to detect refreshes performed
// by other processes (another `claude` instance or a re-login).
//
// File layout: doc.go (this file), types.go (constants and token shapes),
// manager.go (public Manager API), storage.go (keychain and file I/O),
// refresh.go (token refresh and lock).
package oauth
