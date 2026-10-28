package service

import (
	"bytes"
	"encoding/json"
	"novelstudio/presets"
	"testing"
)

func TestNarrativeRecipeRejectsCrossInputEvidenceBeforeConfirmation(t *testing.T) {
	for _, name := range []string{"memory-consolidation", "adaptation-fidelity-review"} {
		t.Run(name, func(t *testing.T) {
			bundle, err := presets.Bundle(name)
			if err != nil {
				t.Fatal(err)
			}
			var example struct{ Input, Output json.RawMessage }
			if err = json.Unmarshal(bundle.Find("examples.json"), &example); err != nil {
				t.Fatal(err)
			}
			key := "shared-" + name
			if err = validateNarrativeRecipeOutput(key, example.Input, example.Output); err != nil {
				t.Fatal("valid built-in example rejected", err)
			}
			changed := bytes.Replace(example.Input, []byte("project-a"), []byte("another-project"), 1)
			if bytes.Equal(changed, example.Input) {
				t.Fatal("mutation did not change fixture")
			}
			if err = validateNarrativeRecipeOutput(key, changed, example.Output); err == nil {
				t.Fatal("schema-valid result ignored frozen input")
			}
		})
	}
}
