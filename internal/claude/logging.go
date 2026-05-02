package claude

import "goodkind.io/clyde/internal/slogger"

var (
	claudeLifecycleLog = slogger.Concern(slogger.ConcernProviderClaudeLifecycle)
	claudeRemoteLog    = slogger.Concern(slogger.ConcernProviderClaudeRemoteControl)
)
