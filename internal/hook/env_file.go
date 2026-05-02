package hook

import (
	"fmt"
	"os"
)

func appendToEnvFile(key, value string) error {
	claudeEnvFile := os.Getenv("CLAUDE_ENV_FILE")
	if claudeEnvFile == "" {
		return nil
	}

	f, err := os.OpenFile(claudeEnvFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		hookLog.Warn("hook.env_file.open_failed",
			"component", "hook",
			"path", claudeEnvFile,
			"err", err,
		)
		return fmt.Errorf("failed to open CLAUDE_ENV_FILE: %w", err)
	}
	defer func() { _ = f.Close() }()

	if _, err := fmt.Fprintf(f, "%s=%s\n", key, value); err != nil {
		hookLog.Warn("hook.env_file.write_failed",
			"component", "hook",
			"path", claudeEnvFile,
			"key", key,
			"err", err,
		)
		return fmt.Errorf("failed to write to CLAUDE_ENV_FILE: %w", err)
	}
	return nil
}

func writeSessionNameToEnv(sessionName string) error {
	return appendToEnvFile("CLYDE_SESSION", sessionName)
}
