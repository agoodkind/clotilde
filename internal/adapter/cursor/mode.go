package cursor

import (
	"strings"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

const permissionsInstructions = `<permissions_instructions>
Filesystem sandboxing defines which files can be read or written. ` + "`sandbox_mode`" + ` is ` + "`danger-full-access`" + `: No filesystem sandboxing - all commands are permitted. Network access is enabled.
Approval policy is currently ` + "`never`" + `. Use the available tools directly when they help complete the task.
</permissions_instructions>`

const toolCallingInstructions = `<tool_calling_instructions>
- When you call a tool, emit complete JSON arguments that satisfy the tool schema.
- Never emit empty tool arguments like {}` + "`" + ` or omit required fields.
- Use the cwd from ` + "`<environment_context>`" + ` to derive sensible absolute paths when a tool schema expects them.
- For file write or edit requests, prefer taking the next reasonable tool action over asking for clarification when the workspace context is sufficient.
- If you need to inspect the workspace before writing, call the appropriate search/read tools with concrete arguments.
</tool_calling_instructions>`

const agentModeInstructions = `<cursor_mode>
You are in Agent Mode. When the user asks you to implement or change code, make the change directly instead of stopping at a plan.
</cursor_mode>`

const planModeInstructions = `<cursor_mode>
You are in Plan Mode. Only edit markdown files while the user is refining the plan. If the user explicitly asks you to build, implement, or write the code now, switch to agent mode before making code changes.
</cursor_mode>`

type Mode string

const (
	ModeAgent Mode = "agent"
	ModePlan  Mode = "plan"
)

type PromptContext struct {
	Mode              Mode
	InstructionPrefix string
	DeveloperSections []string
	UserSections      []string
}

func DetectMode(req Request) Mode {
	if req.Mode != "" {
		return req.Mode
	}
	return ModeAgent
}

func CodexPromptContext(req Request, systemSections []string, environmentText string) PromptContext {
	mode := DetectMode(req)
	developerSections := make([]string, 0, len(systemSections)+2)
	developerSections = append(developerSections, strings.TrimSpace(permissionsInstructions))
	developerSections = append(developerSections, strings.TrimSpace(toolCallingInstructions))
	for _, section := range systemSections {
		if trimmed := strings.TrimSpace(section); trimmed != "" {
			developerSections = append(developerSections, trimmed)
		}
	}

	userSections := make([]string, 0, 1)
	if trimmed := strings.TrimSpace(environmentText); trimmed != "" {
		userSections = append(userSections, trimmed)
	}

	instructionPrefix := strings.TrimSpace(agentModeInstructions)
	if mode == ModePlan {
		instructionPrefix = strings.TrimSpace(planModeInstructions)
	}

	return PromptContext{
		Mode:              mode,
		InstructionPrefix: instructionPrefix,
		DeveloperSections: developerSections,
		UserSections:      userSections,
	}
}

func FlattenContent(raw []byte) string {
	return adapteropenai.FlattenContent(raw)
}
