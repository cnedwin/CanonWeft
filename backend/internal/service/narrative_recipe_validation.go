package service

import (
	"encoding/json"
	"novelstudio/presets"
)

func narrativeRecipePreset(key string) string {
	switch key {
	case "shared-memory-consolidation":
		return "memory-consolidation"
	case "shared-adaptation-fidelity-review":
		return "adaptation-fidelity-review"
	default:
		return ""
	}
}

// Built-in v1 narrative protocols require cross-input evidence checks as well
// as JSON shape checks. This still does not authorize source claims or adoption.
func validateNarrativeRecipeOutput(key string, input, output json.RawMessage) error {
	name := narrativeRecipePreset(key)
	if name == "" {
		return nil
	}
	return presets.ValidateMemoryProposal(name, input, output)
}
