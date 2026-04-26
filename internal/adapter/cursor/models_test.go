package cursor

import (
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestNormalizeModelAliasPreservesForegroundAliasParity(t *testing.T) {
	testCases := map[string]string{
		"clyde-gpt-5.4-1m-medium":  "clyde-gpt-5.4-1m-medium",
		"clyde-gpt-5.5-1m-xhigh":   "clyde-gpt-5.5-1m-xhigh",
		"clyde-codex-gpt-5.4-high": "clyde-codex-gpt-5.4",
		"clyde-gpt-5.4":            "clyde-gpt-5.4",
		"gpt-5.4":                  "gpt-5.4",
		" clyde-gpt-5.4-1m-medium ": "clyde-gpt-5.4-1m-medium",
	}

	for rawModel, want := range testCases {
		if got := NormalizeModelAlias(rawModel); got != want {
			t.Fatalf("NormalizeModelAlias(%q) = %q want %q", rawModel, got, want)
		}
	}
}

func TestTranslateRequestCarriesNormalizedModel(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Model: "gpt-5.4",
	})

	if req.NormalizedModel != "gpt-5.4" {
		t.Fatalf("NormalizedModel=%q want %q", req.NormalizedModel, "gpt-5.4")
	}
	if req.Mode != ModeAgent {
		t.Fatalf("Mode=%q want %q", req.Mode, ModeAgent)
	}
	if req.PathKind != RequestPathForeground {
		t.Fatalf("PathKind=%q want %q", req.PathKind, RequestPathForeground)
	}
}

func TestRequestPathClassifiesSubagentRequests(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Tools: []adapteropenai.Tool{{
			Type: "function",
			Function: adapteropenai.ToolFunctionSchema{
				Name: "Subagent",
			},
		}},
	})

	if len(req.RawToolNames) != 1 || req.RawToolNames[0] != "Subagent" {
		t.Fatalf("RawToolNames=%v", req.RawToolNames)
	}
	if !req.CanSpawnAgent {
		t.Fatalf("CanSpawnAgent=false want true")
	}
	if req.PathKind != RequestPathForeground {
		t.Fatalf("PathKind=%q want %q", req.PathKind, RequestPathForeground)
	}
}

func TestRequestPathDefaultsToForeground(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Model: "clyde-gpt-5.4",
	})

	if req.PathKind != RequestPathForeground {
		t.Fatalf("PathKind=%q want %q", req.PathKind, RequestPathForeground)
	}
}

func TestTranslateRequestCarriesExplicitCursorCapabilities(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Tools: []adapteropenai.Tool{
			{
				Type: "function",
				Function: adapteropenai.ToolFunctionSchema{
					Name: "CreatePlan",
				},
			},
			{
				Type: "function",
				Function: adapteropenai.ToolFunctionSchema{
					Name: "SwitchMode",
				},
			},
		},
	})

	if req.Mode != ModePlan {
		t.Fatalf("Mode=%q want %q", req.Mode, ModePlan)
	}
	if !req.CanSwitchMode {
		t.Fatalf("CanSwitchMode=false want true")
	}
	if req.CanSpawnAgent {
		t.Fatalf("CanSpawnAgent=true want false")
	}
}
