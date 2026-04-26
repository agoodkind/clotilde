package fallback

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type TranscriptResumeConfig struct {
	WorkspaceDir string
	ClaudeHome   string
	Now          func() time.Time
}

type TranscriptResumeResult struct {
	Attempted    bool
	Resumed      bool
	SessionID    string
	WorkspaceDir string
	Path         string
	PriorTurns   int
	Err          error
}

// PrepareTranscriptResume writes a synthesized Claude Code transcript
// for all turns before the final user turn and mutates req so the next
// spawn uses `--resume <session-id>`. The caller owns policy for when
// to attempt this; this package owns the Claude transcript mechanics.
func PrepareTranscriptResume(req *Request, cfg TranscriptResumeConfig) TranscriptResumeResult {
	if req == nil {
		return TranscriptResumeResult{
			Attempted: true,
			Err:       fmt.Errorf("nil fallback request"),
		}
	}
	res := TranscriptResumeResult{
		Attempted:    true,
		SessionID:    req.SessionID,
		WorkspaceDir: cfg.WorkspaceDir,
	}
	if len(req.Messages) == 0 || req.SessionID == "" || cfg.WorkspaceDir == "" {
		res.Attempted = false
		return res
	}
	if err := os.MkdirAll(cfg.WorkspaceDir, 0o755); err != nil {
		res.Err = fmt.Errorf("mkdir workspace: %w", err)
		return res
	}
	lastUser := -1
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			lastUser = i
			break
		}
	}
	if lastUser <= 0 {
		res.Err = fmt.Errorf("no prior turns to synthesize")
		return res
	}
	res.PriorTurns = lastUser
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	lines, err := SynthesizeTranscript(req.Messages[:lastUser], req.SessionID, cfg.WorkspaceDir, now())
	if err != nil {
		res.Err = fmt.Errorf("synthesize: %w", err)
		return res
	}
	claudeHome := cfg.ClaudeHome
	if claudeHome == "" {
		claudeHome = ClaudeConfigHome()
	}
	path := TranscriptPath(claudeHome, cfg.WorkspaceDir, req.SessionID)
	if err := WriteTranscript(path, lines); err != nil {
		res.Err = fmt.Errorf("write: %w", err)
		return res
	}
	req.Resume = true
	req.WorkspaceDir = cfg.WorkspaceDir
	res.Path = path
	res.Resumed = true
	return res
}

// ClaudeConfigHome resolves $CLAUDE_CONFIG_HOME, falling back to
// ~/.claude. Matches Claude Code's session storage resolution.
func ClaudeConfigHome() string {
	if v := os.Getenv("CLAUDE_CONFIG_HOME"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".claude"
	}
	return filepath.Join(home, ".claude")
}
