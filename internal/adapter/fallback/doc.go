// Package fallback drives the local `claude` CLI in
// `-p --output-format stream-json` mode as the third adapter
// backend. It exists so the OpenAI adapter can either explicitly
// route an alias through `claude -p` or escalate to it when the
// direct-OAuth path returns an error. The package is deliberately
// isolated from the parent `adapter` package so the registry and
// HTTP layers stay slim and so the subprocess details (env
// suppression, scratch dir, stream-json parser) live in one place.
//
// There are no compiled-in defaults: the parent registry validates
// every required field on construction. This package assumes a
// fully populated Config and panics nowhere; bad input surfaces as
// an error from Collect or Stream.
//
// File layout: doc.go (this file), types.go (request/result shapes and
// stream-json wire mirrors), config.go (Config, Client, Collect, Stream),
// spawn.go (subprocess and argv), streamparser.go (stdout JSONL),
// tools.go (tool preamble and envelope parsing).
package fallback
