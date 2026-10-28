package presets

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"testing"
)

func TestCreativeContractsRejectUnsupportedEvidenceClaims(t *testing.T) {
	for _, name := range []string{"character-naming", "genre-research"} {
		t.Run(name, func(t *testing.T) {
			b, err := Bundle(name)
			if err != nil {
				t.Fatal(err)
			}
			var schema any
			if err = json.Unmarshal(b.Find("output.schema.json"), &schema); err != nil {
				t.Fatal(err)
			}
			compiler := jsonschema.NewCompiler()
			if err = compiler.AddResource("https://studio.invalid/output.json", schema); err != nil {
				t.Fatal(err)
			}
			compiled, err := compiler.Compile("https://studio.invalid/output.json")
			if err != nil {
				t.Fatal(err)
			}
			var examples map[string]any
			if err = json.Unmarshal(b.Find("examples.json"), &examples); err != nil {
				t.Fatal(err)
			}
			output := examples["output"].(map[string]any)
			if name == "character-naming" {
				item := output["candidates"].([]any)[0].(map[string]any)
				for _, origin := range []string{"collected", "adapted"} {
					item["origin"] = origin
					if compiled.Validate(output) == nil {
						t.Fatal("accepted sourceless", origin)
					}
				}
				item["origin"] = "invented"
				item["confidence"] = "supported"
			} else {
				output["fact_cards"] = []any{map[string]any{"claim": "unsupported claim", "status": "supported", "sources": []any{}, "era_scope": "unknown", "region_scope": "unknown", "limitations": "unknown", "writing_use": "unconfirmed", "review_after": "before use"}}
			}
			if compiled.Validate(output) == nil {
				t.Fatal("accepted supported evidence without sources")
			}
		})
	}
}
