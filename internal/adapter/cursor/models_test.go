package cursor

import (
	"testing"

	adapteropenai "goodkind.io/clyde/internal/adapter/openai"
)

func TestNormalizeModelAliasPreservesForegroundAliasParity(t *testing.T) {
	testCases := map[string]string{
		"clyde-gpt-5.4-1m-medium":  "clyde-gpt-5.4-1m-medium",
		"clyde-codex-5.5-xhigh":    "clyde-codex-5.5-xhigh",
		"clyde-codex-gpt-5.4-high": "clyde-codex-gpt-5.4-high",
		"clyde-gpt-5.4":            "clyde-gpt-5.4",
		"gpt-5.4":                  "gpt-5.4",
	}

	for rawModel, want := range testCases {
		if got := NormalizeModelAlias(rawModel); got != want {
			t.Fatalf("NormalizeModelAlias(%q) = %q want %q", rawModel, got, want)
		}
	}
	if got := NormalizeModelAlias(" clyde-gpt-5.4-1m-medium "); got != "clyde-gpt-5.4-1m-medium" {
		t.Fatalf("NormalizeModelAlias trims surrounding whitespace = %q", got)
	}
}

func TestNormalizeSessionSettingsModelPreservesDeclarativeAliases(t *testing.T) {
	testCases := map[string]string{
		"clyde-gpt-5.4-1m-medium": "clyde-gpt-5.4-1m-medium",
		"clyde-codex-5.5-xhigh":   "clyde-codex-5.5-xhigh",
		"clyde-gpt-5.4":           "clyde-gpt-5.4",
	}

	for rawModel, want := range testCases {
		if got := NormalizeSessionSettingsModel(rawModel); got != want {
			t.Fatalf("NormalizeSessionSettingsModel(%q) = %q want %q", rawModel, got, want)
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

func TestRequestPathClassifiesTaskAsSubagentCapability(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Tools: []adapteropenai.Tool{{
			Type: "function",
			Function: adapteropenai.ToolFunctionSchema{
				Name: "Task",
			},
		}},
	})

	if len(req.RawToolNames) != 1 || req.RawToolNames[0] != "Task" {
		t.Fatalf("RawToolNames=%v", req.RawToolNames)
	}
	if !req.CanSpawnAgent {
		t.Fatalf("CanSpawnAgent=false want true")
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

func TestRequestPathUsesCursorMetadata(t *testing.T) {
	testCases := []struct {
		name     string
		metadata string
		want     RequestPathKind
	}{
		{
			name:     "resume",
			metadata: `{"cursorResumeTaskId":"task-123"}`,
			want:     RequestPathResume,
		},
		{
			name:     "subagent",
			metadata: `{"cursorSubagentId":"agent-123"}`,
			want:     RequestPathSubagent,
		},
		{
			name:     "background",
			metadata: `{"runInBackground":true}`,
			want:     RequestPathBackground,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := TranslateRequest(adapteropenai.ChatRequest{
				Model:    "gpt-5.4",
				Metadata: []byte(tc.metadata),
			})

			if req.PathKind != tc.want {
				t.Fatalf("PathKind=%q want %q", req.PathKind, tc.want)
			}
		})
	}
}

func TestTranslateRequestExtractsGenerationID(t *testing.T) {
	t.Parallel()

	req := adapteropenai.ChatRequest{
		Model:    "gpt-5.5",
		Messages: []adapteropenai.ChatMessage{{Role: "user", Content: []byte(`"hi"`)}},
		Metadata: []byte(`{
			"cursorConversationId":"conv-1",
			"generation_id":"gen-1"
		}`),
	}

	got := TranslateRequest(req)
	if got.GenerationID != "gen-1" {
		t.Fatalf("GenerationID=%q want gen-1", got.GenerationID)
	}
}

func TestRequestPathFallsBackToObservedPromptMarkers(t *testing.T) {
	req := TranslateRequest(adapteropenai.ChatRequest{
		Model: "gpt-5.4",
		Messages: []adapteropenai.ChatMessage{{
			Role:    "system",
			Content: []byte(`"You are the forked subagent; continue executing your task."`),
		}},
	})

	if req.PathKind != RequestPathSubagent {
		t.Fatalf("PathKind=%q want %q", req.PathKind, RequestPathSubagent)
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
