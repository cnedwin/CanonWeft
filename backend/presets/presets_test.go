package presets

import (
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"novelstudio/internal/skills"
	"testing"
)

func TestBundledMethodsHaveValidContractsAndExamples(t *testing.T) {
	names, err := Names()
	if err != nil || len(names) == 0 {
		t.Fatal(names, err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			bundle, err := Bundle(name)
			if err != nil {
				t.Fatal(err)
			}
			report, err := skills.Analyze(bundle)
			if err != nil || report.Name != name || len(report.LicenseFiles) == 0 {
				t.Fatal(report, err)
			}
			var examples map[string]any
			if err = json.Unmarshal(bundle.Find("examples.json"), &examples); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"input", "output"} {
				var schema any
				if err = json.Unmarshal(bundle.Find(kind+".schema.json"), &schema); err != nil {
					t.Fatal(err)
				}
				compiler := jsonschema.NewCompiler()
				location := "https://studio.invalid/" + kind + ".json"
				if err = compiler.AddResource(location, schema); err != nil {
					t.Fatal(err)
				}
				compiled, err := compiler.Compile(location)
				if err != nil {
					t.Fatal(err)
				}
				if err = compiled.Validate(examples[kind]); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
