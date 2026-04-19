// Package oauth reads, caches, and refreshes OAuth tokens stored in the
// macOS keychain or in a fallback credentials file under the CLI config dir.
//
// The adapter uses these tokens for Bearer auth on the direct HTTP chat
// path. Endpoints and client metadata come from cfg, not from compiled-in
// literals.
//
// Cross-process refresh uses a file lock under the credentials directory.
//
// File layout: doc.go (this file), types.go, manager.go, storage.go,
// refresh.go.
package oauth
