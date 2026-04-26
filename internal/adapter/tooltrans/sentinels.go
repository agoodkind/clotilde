package tooltrans

import (
	"regexp"
	"strings"
)

var noticeSentinelRE = regexp.MustCompile(`(?s)<!--clyde-notice-->.*?<!--/clyde-notice-->\s*`)
var activitySentinelRE = regexp.MustCompile(`(?s)<!--clyde-activity-->.*?<!--/clyde-activity-->\s*`)
var thinkingBlockquoteRE = regexp.MustCompile(`(?s)<!--clyde-thinking-->.*?<!--/clyde-thinking-->\s*`)

// StripNoticeSentinel removes the clyde notice envelope.
func StripNoticeSentinel(text string) string {
	if text == "" {
		return ""
	}
	return noticeSentinelRE.ReplaceAllString(text, "")
}

// StripActivitySentinel removes the shared activity envelope.
func StripActivitySentinel(text string) string {
	if text == "" {
		return ""
	}
	return activitySentinelRE.ReplaceAllString(text, "")
}

// StripThinkingSentinel removes the clyde thinking envelope.
func StripThinkingSentinel(text string) string {
	return stripThinkingBlockquote(text)
}

func stripThinkingBlockquote(text string) string {
	if !strings.Contains(text, "<!--clyde-thinking-->") {
		return text
	}
	return thinkingBlockquoteRE.ReplaceAllString(text, "")
}
