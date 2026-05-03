// Package claude exposes Claude provider helpers shared outside lifecycle code.
package claude

import (
	"path/filepath"
	"strings"
)

// ProjectDir converts a clyde root path to Claude Code's project directory format.
// Claude Code stores project data in ~/.claude/projects/<encoded-path>/
// where the path is encoded by replacing / and . with -
//
// Example:
//
//	/home/user/project/.claude/clyde -> ~/.claude/projects/-home-user-project
func ProjectDir(clydeRoot string) string {
	// Get the project root (parent of .claude/clyde)
	projectRoot := filepath.Dir(filepath.Dir(clydeRoot))

	// Convert path: replace / and . with -
	encoded := strings.ReplaceAll(projectRoot, "/", "-")
	encoded = strings.ReplaceAll(encoded, ".", "-")

	return encoded
}

// TranscriptPath returns the path to a session's transcript file in Claude's storage.
// Format: ~/.claude/projects/<project-dir>/<session-id>.jsonl
func TranscriptPath(homeDir, clydeRoot, sessionID string) string {
	projectDir := ProjectDir(clydeRoot)
	return filepath.Join(homeDir, ".claude", "projects", projectDir, sessionID+".jsonl")
}
