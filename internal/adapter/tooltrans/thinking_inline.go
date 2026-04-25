package tooltrans

import "strings"

// ThinkingInlineOpen renders an empty Cursor-friendly thinking block.
// Use this when the backend exposes that reasoning happened but does not
// provide displayable reasoning text.
func ThinkingInlineOpen() string {
	return "<!--clyde-thinking-->\n> **💭 Thinking**\n> \n"
}

// FormatThinkingInlineDelta renders thinking text as Cursor-friendly
// streamed markdown wrapped in clyde-thinking sentinels.
func FormatThinkingInlineDelta(open bool, text string) string {
	if !open {
		return strings.ReplaceAll(text, "\n", "\n> ")
	}
	return ThinkingInlineOpen() + "> " + strings.ReplaceAll(text, "\n", "\n> ")
}

// ThinkingInlineClose emits the closing sentinel and a trailing blank line.
func ThinkingInlineClose() string {
	return "\n<!--/clyde-thinking-->\n\n"
}
