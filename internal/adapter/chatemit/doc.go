// Package chatemit contains shared adapters for OpenAI-compatible chat
// streaming and completion responses.
//
// The package intentionally keeps logging and wire-shape helpers separate
// from transport handlers so oauth and fallback handlers share behavior
// without drifting.
package chatemit
