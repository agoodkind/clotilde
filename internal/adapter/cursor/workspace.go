package cursor

import (
	"regexp"
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

var workspacePathRE = regexp.MustCompile(`(?m)\bWorkspace Path:\s*([^\n<]+)`)

func WorkspacePath(req Request) string {
	return req.WorkspacePath
}

func WorkspacePathFromOpenAI(req adapteropenai.ChatRequest) string {
	return TranslateRequest(req).WorkspacePath
}

func workspacePath(req adapteropenai.ChatRequest) string {
	sources := make([]string, 0, len(req.Messages)+1)
	for _, msg := range req.Messages {
		if text := adapteropenai.FlattenContent(msg.Content); text != "" {
			sources = append(sources, text)
		}
	}
	if len(req.Input) > 0 {
		sources = append(sources, string(req.Input))
	}
	for _, source := range sources {
		match := workspacePathRE.FindStringSubmatch(source)
		if len(match) < 2 {
			continue
		}
		if path := strings.TrimSpace(match[1]); path != "" {
			return path
		}
	}
	return ""
}
