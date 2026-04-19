package hook

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func isHookExecuted(marker string) bool {
	if os.Getenv("CLYDE_HOOK_EXECUTED") == marker {
		return true
	}
	return readLastEnvFileValue("CLYDE_HOOK_EXECUTED") == marker
}

func markHookExecuted(marker string) {
	_ = appendToEnvFile("CLYDE_HOOK_EXECUTED", marker)
}

// hasAncestorHook walks the parent process tree and returns true if
// any ancestor's command line already contains "clyde hook
// sessionstart". This is a coarse loop-breaker for the
// daemon -> claude -p -> SessionStart -> clyde hook -> daemon cycle:
// the per-session marker can't catch it because each spawned claude
// gets a new SessionID, so we look at the process tree instead.
//
// Best effort: if `ps` is missing or output looks unexpected, we
// return false so the hook still runs.
func hasAncestorHook() bool {
	pid := os.Getppid()
	for depth := 0; depth < 32 && pid > 1; depth++ {
		cmdline, ppid, ok := psLookup(pid)
		if !ok {
			return false
		}
		if strings.Contains(cmdline, "clyde hook sessionstart") {
			return true
		}
		if ppid == pid {
			return false
		}
		pid = ppid
	}
	return false
}

// psLookup queries `ps` for one pid and returns its full command line
// and parent pid. Returns ok=false if the lookup fails for any reason.
func psLookup(pid int) (cmdline string, ppid int, ok bool) {
	out, err := exec.Command("ps", "-o", "ppid=,command=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return "", 0, false
	}
	line := strings.TrimSpace(string(out))
	if line == "" {
		return "", 0, false
	}
	parts := strings.SplitN(line, " ", 2)
	if len(parts) < 2 {
		return "", 0, false
	}
	parsedPpid, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return "", 0, false
	}
	return strings.TrimSpace(parts[1]), parsedPpid, true
}

func readLastEnvFileValue(key string) string {
	claudeEnvFile := os.Getenv("CLAUDE_ENV_FILE")
	if claudeEnvFile == "" {
		return ""
	}

	content, err := os.ReadFile(claudeEnvFile)
	if err != nil {
		return ""
	}

	prefix := key + "="
	var lastValue string
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, prefix); ok {
			lastValue = after
		}
	}
	return lastValue
}
