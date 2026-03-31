package claude

import "fmt"

// Hook represents a single hook command configuration.
type Hook struct {
	Type    string `json:"type"`
	Command string `json:"command"`
}

// HookMatcher represents a hook matcher with associated hooks.
type HookMatcher struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []Hook `json:"hooks"`
}

// HookConfig represents the hook configuration for Claude Code settings.
type HookConfig struct {
	SessionStart []HookMatcher `json:"SessionStart,omitempty"`
	Stop         []HookMatcher `json:"Stop,omitempty"`
	Notification []HookMatcher `json:"Notification,omitempty"`
	PreToolUse   []HookMatcher `json:"PreToolUse,omitempty"`
	PostToolUse  []HookMatcher `json:"PostToolUse,omitempty"`
}

// GenerateHookConfig generates the hook configuration for clotilde.
// Returns a HookConfig that should be merged into .claude/settings.json.
func GenerateHookConfig(clotildeBinaryPath string) HookConfig {
	sessionStartCommand := fmt.Sprintf("%s hook sessionstart", clotildeBinaryPath)

	return HookConfig{
		SessionStart: []HookMatcher{
			{
				Hooks: []Hook{
					{
						Type:    "command",
						Command: sessionStartCommand,
					},
				},
			},
		},
	}
}
