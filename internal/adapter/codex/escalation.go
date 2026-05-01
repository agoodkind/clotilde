package codex

import (
	"regexp"
	"slices"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

var writeIntentWord = regexp.MustCompile(`(?i)\b(write|save|create|edit|update|modify|patch|apply|commit)\b`)

func HasWriteIntent(req adapteropenai.ChatRequest) bool {
	if len(req.Tools) == 0 {
		return false
	}
	for _, message := range slices.Backward(req.Messages) {
		if !strings.EqualFold(message.Role, "user") {
			continue
		}
		text := strings.TrimSpace(adapteropenai.FlattenContent(message.Content))
		return writeIntentWord.MatchString(text)
	}
	return false
}
