package cursor

import (
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

func mustRawContent(raw string) []byte {
	return []byte(raw)
}
