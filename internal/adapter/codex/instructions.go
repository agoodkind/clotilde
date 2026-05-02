package codex

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed codex_model_instructions.json
var modelInstructionsJSON []byte

var modelInstructions = loadModelInstructions()

type modelInstructionCatalog struct {
	Models []struct {
		Slug             string `json:"slug"`
		BaseInstructions string `json:"base_instructions"`
	} `json:"models"`
}

func BaseInstructions(modelName string) string {
	return strings.TrimSpace(modelInstructions[strings.TrimSpace(modelName)])
}

func loadModelInstructions() map[string]string {
	var catalog modelInstructionCatalog
	if err := json.Unmarshal(modelInstructionsJSON, &catalog); err != nil {
		codexConcernLog.Logger().Error("adapter.codex.instructions_catalog.parse_failed",
			"component", "adapter",
			"subcomponent", "codex",
			"err", err,
		)
		return nil
	}
	out := make(map[string]string, len(catalog.Models))
	for _, model := range catalog.Models {
		slug := strings.TrimSpace(model.Slug)
		if slug == "" {
			continue
		}
		text := strings.TrimSpace(model.BaseInstructions)
		if text == "" {
			continue
		}
		out[slug] = text
	}
	return out
}
