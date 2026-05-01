package claude

import claudeartifacts "goodkind.io/clyde/internal/claude/artifacts"

// DeletedFiles contains information about files deleted during cleanup.
type DeletedFiles = claudeartifacts.DeletedFiles

// DeleteSessionData removes Claude Code transcript and agent logs for a session.
// If transcriptPath is provided, uses it directly. Otherwise computes it from clydeRoot.
// Returns DeletedFiles with info about what was deleted.
func DeleteSessionData(clydeRoot, sessionID, transcriptPath string) (*DeletedFiles, error) {
	return claudeartifacts.DeleteSessionData(clydeRoot, sessionID, transcriptPath)
}
