// Package artifacts removes Claude-owned files associated with Clyde sessions.
package artifacts

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"goodkind.io/clyde/internal/session"
	"goodkind.io/clyde/internal/util"
)

// DeletedFiles contains information about files deleted during cleanup.
type DeletedFiles struct {
	Transcript []string
	AgentLogs  []string
}

// DeleteSessionArtifacts removes all Claude Code artifacts for the session's
// current and historical provider identities.
func DeleteSessionArtifacts(clydeRoot string, sess *session.Session) (*DeletedFiles, error) {
	deleted := &DeletedFiles{
		Transcript: []string{},
		AgentLogs:  []string{},
	}
	if sess == nil {
		return deleted, fmt.Errorf("nil session")
	}
	current, err := DeleteSessionData(clydeRoot, sess.Metadata.ProviderSessionID(), sess.Metadata.ProviderTranscriptPath())
	if err != nil {
		return deleted, err
	}
	deleted.Transcript = append(deleted.Transcript, current.Transcript...)
	deleted.AgentLogs = append(deleted.AgentLogs, current.AgentLogs...)
	currentID := strings.TrimSpace(sess.Metadata.ProviderSessionID())
	for _, identity := range session.HistoricalIdentities(sess) {
		if identity.ID == currentID {
			continue
		}
		previous, previousErr := DeleteSessionData(clydeRoot, identity.ID, "")
		if previousErr != nil {
			return deleted, previousErr
		}
		deleted.Transcript = append(deleted.Transcript, previous.Transcript...)
		deleted.AgentLogs = append(deleted.AgentLogs, previous.AgentLogs...)
	}
	return deleted, nil
}

// DeleteSessionData removes Claude Code transcript and agent logs for a single
// provider session id.
func DeleteSessionData(clydeRoot, sessionID, transcriptPath string) (*DeletedFiles, error) {
	deleted := &DeletedFiles{
		Transcript: []string{},
		AgentLogs:  []string{},
	}

	var claudeProjectDir string
	if transcriptPath != "" {
		if util.FileExists(transcriptPath) {
			if err := os.Remove(transcriptPath); err != nil {
				return deleted, fmt.Errorf("failed to delete transcript: %w", err)
			}
			deleted.Transcript = append(deleted.Transcript, transcriptPath)
		}
		claudeProjectDir = filepath.Dir(transcriptPath)
	} else {
		projectDir := projectDir(clydeRoot)
		homeDir, err := util.HomeDir()
		if err != nil {
			return deleted, fmt.Errorf("failed to get home directory: %w", err)
		}
		claudeProjectDir = filepath.Join(homeDir, ".claude", "projects", projectDir)
		transcriptPath := filepath.Join(claudeProjectDir, sessionID+".jsonl")
		if util.FileExists(transcriptPath) {
			if err := os.Remove(transcriptPath); err != nil {
				return deleted, fmt.Errorf("failed to delete transcript: %w", err)
			}
			deleted.Transcript = append(deleted.Transcript, transcriptPath)
		}
	}

	agentLogs, err := deleteAgentLogs(claudeProjectDir, sessionID)
	if err != nil {
		return deleted, err
	}
	deleted.AgentLogs = agentLogs

	claudeCleanupLog.Logger().Info("claude.cleanup.session_data.completed",
		"component", "claude",
		"subcomponent", "cleanup",
		"session_id", sessionID,
		"transcript_removed", len(deleted.Transcript),
		"agent_logs_removed", len(deleted.AgentLogs),
	)

	return deleted, nil
}

func deleteAgentLogs(claudeProjectDir, sessionID string) ([]string, error) {
	deletedLogs := []string{}
	if !util.DirExists(claudeProjectDir) {
		return deletedLogs, nil
	}
	pattern := filepath.Join(claudeProjectDir, "agent-*.jsonl")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return deletedLogs, fmt.Errorf("failed to find agent logs: %w", err)
	}
	for _, logPath := range matches {
		containsSession, err := fileContainsSessionID(logPath, sessionID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to check %s: %v\n", logPath, err)
			continue
		}
		if containsSession {
			if err := os.Remove(logPath); err != nil {
				return deletedLogs, fmt.Errorf("failed to delete agent log %s: %w", logPath, err)
			}
			deletedLogs = append(deletedLogs, logPath)
		}
	}
	return deletedLogs, nil
}

func fileContainsSessionID(path, sessionID string) (bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), sessionID) {
			return true, nil
		}
	}
	return false, scanner.Err()
}

func projectDir(clydeRoot string) string {
	projectRoot := filepath.Dir(filepath.Dir(clydeRoot))
	encoded := strings.ReplaceAll(projectRoot, "/", "-")
	return strings.ReplaceAll(encoded, ".", "-")
}
