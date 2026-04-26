package cursor

import (
	"strings"
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestDetectModeUsesCreatePlanTool(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Tools: []adapteropenai.Tool{{
			Type: "function",
			Function: adapteropenai.ToolFunctionSchema{
				Name: "CreatePlan",
			},
		}},
	})

	if got := DetectMode(req); got != ModePlan {
		t.Fatalf("mode=%q want %q", got, ModePlan)
	}
}

func TestDetectModeDefaultsToAgentWithoutCreatePlanTool(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Messages: []adapteropenai.ChatMessage{{
			Role:    "system",
			Content: mustRawContent(`"<plan_mode_guardrails>stay in markdown</plan_mode_guardrails>"`),
		}},
	})

	if got := DetectMode(req); got != ModeAgent {
		t.Fatalf("mode=%q want %q", got, ModeAgent)
	}
}

func TestCodexPromptContextIncludesCursorDeveloperSections(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{})
	got := CodexPromptContext(req, []string{"system rules"}, "<environment_context><cwd>/repo</cwd></environment_context>")
	if got.Mode != ModeAgent {
		t.Fatalf("mode=%q want %q", got.Mode, ModeAgent)
	}
	if !strings.Contains(got.InstructionPrefix, "Agent Mode") {
		t.Fatalf("instruction prefix=%q", got.InstructionPrefix)
	}
	if len(got.DeveloperSections) < 3 {
		t.Fatalf("developer sections=%d", len(got.DeveloperSections))
	}
	joined := strings.Join(got.DeveloperSections, "\n\n")
	if !strings.Contains(joined, "<permissions instructions>") || !strings.Contains(joined, "<tool_calling_instructions>") || !strings.Contains(joined, "system rules") {
		t.Fatalf("developer sections=%q", joined)
	}
	if len(got.UserSections) != 1 || !strings.Contains(got.UserSections[0], "<environment_context>") {
		t.Fatalf("user sections=%v", got.UserSections)
	}
}

func TestCodexPromptContextUsesPlanModeInstructions(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Tools: []adapteropenai.Tool{{
			Type: "function",
			Function: adapteropenai.ToolFunctionSchema{
				Name: "CreatePlan",
			},
		}},
	})

	got := CodexPromptContext(req, nil, "")
	if got.Mode != ModePlan {
		t.Fatalf("mode=%q want %q", got.Mode, ModePlan)
	}
	if !strings.Contains(got.InstructionPrefix, "Plan Mode") || !strings.Contains(got.InstructionPrefix, "Only edit markdown files") {
		t.Fatalf("instruction prefix=%q", got.InstructionPrefix)
	}
}

func mustRawContent(raw string) []byte {
	return []byte(raw)
}
