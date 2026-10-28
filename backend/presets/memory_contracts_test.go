package presets

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"novelstudio/internal/domain"
)

func TestMemoryContractsRejectUnsupportedClaims(t *testing.T) {
	cases := []struct {
		name string
		edit func(map[string]any)
	}{
		{"memory-consolidation", func(v map[string]any) { v["status"] = "CONFIRMED" }},
		{"memory-consolidation", func(v map[string]any) { v["candidates"].([]any)[0].(map[string]any)["evidence"] = []any{} }},
		{"memory-consolidation", func(v map[string]any) { delete(v["candidates"].([]any)[0].(map[string]any), "scope") }},
		{"memory-consolidation", func(v map[string]any) { v["candidates"].([]any)[0].(map[string]any)["kind"] = "belief" }},
		{"adaptation-fidelity-review", func(v map[string]any) { v["checks"].([]any)[0].(map[string]any)["source_evidence"] = []any{} }},
		{"adaptation-fidelity-review", func(v map[string]any) { v["checks"].([]any)[0].(map[string]any)["target_evidence"] = []any{} }},
		{"adaptation-fidelity-review", func(v map[string]any) {
			v["verdict"] = "PASS"
			v["limitations"] = []any{}
			v["checks"].([]any)[0].(map[string]any)["result"] = "UNKNOWN"
		}},
	}
	for i, tc := range cases {
		t.Run(tc.name+string(rune('a'+i)), func(t *testing.T) {
			bundle, err := Bundle(tc.name)
			if err != nil {
				t.Fatal(err)
			}
			schema, err := domain.CompileCapabilitySchema(bundle.Find("output.schema.json"))
			if err != nil {
				t.Fatal(err)
			}
			var examples map[string]any
			if err := json.Unmarshal(bundle.Find("examples.json"), &examples); err != nil {
				t.Fatal(err)
			}
			output := examples["output"].(map[string]any)
			if err := schema.Validate(output); err != nil {
				t.Fatal(err)
			}
			tc.edit(output)
			if err := schema.Validate(output); err == nil {
				t.Fatal("accepted unsupported claim")
			}
		})
	}
}

func TestMemoryProposalCrossInputEvidenceGuards(t *testing.T) {
	cases := []struct {
		name   string
		preset string
		edit   func(map[string]any, map[string]any)
	}{
		{"stale base", "memory-consolidation", func(in, out map[string]any) { out["base_revision"] = "stale" }},
		{"tampered source text", "memory-consolidation", func(in, out map[string]any) { first(in, "sources")["text"] = "伪造正文" }},
		{"wrong revision", "memory-consolidation", func(in, out map[string]any) { first(first(out, "candidates"), "evidence")["revision"] = "stale" }},
		{"wrong rune", "memory-consolidation", func(in, out map[string]any) { first(first(out, "candidates"), "evidence")["start_rune"] = 0 }},
		{"unseen source", "memory-consolidation", func(in, out map[string]any) { first(first(out, "candidates"), "evidence")["resource_id"] = "invented" }},
		{"public leak", "memory-consolidation", func(in, out map[string]any) {
			first(out, "candidates")["scope"].(map[string]any)["visibility"] = "public"
		}},
		{"extra viewer", "memory-consolidation", func(in, out map[string]any) {
			first(out, "candidates")["scope"].(map[string]any)["known_by"] = []any{"author", "sister"}
		}},
		{"early knowledge", "memory-consolidation", func(in, out map[string]any) { first(out, "candidates")["scope"].(map[string]any)["known_from"] = 0 }},
		{"future source", "memory-consolidation", func(in, out map[string]any) { first(in, "sources")["scope"].(map[string]any)["known_from"] = 99 }},
		{"other branch", "memory-consolidation", func(in, out map[string]any) { first(in, "sources")["scope"].(map[string]any)["branch_id"] = "other" }},
		{"duplicate memory", "memory-consolidation", func(in, out map[string]any) {
			out["candidates"].([]any)[1].(map[string]any)["id"] = first(out, "candidates")["id"]
		}},
		{"incomplete coverage", "adaptation-fidelity-review", func(in, out map[string]any) { out["checks"] = out["checks"].([]any)[:1] }},
		{"unknown coverage", "adaptation-fidelity-review", func(in, out map[string]any) { first(out, "checks")["requirement_id"] = "invented" }},
		{"source masquerades as target", "adaptation-fidelity-review", func(in, out map[string]any) {
			first(out, "checks")["target_evidence"] = first(out, "checks")["source_evidence"]
		}},
		{"missing inferred from excerpt", "adaptation-fidelity-review", func(in, out map[string]any) { first(out, "checks")["result"] = "MISSING" }},
		{"unsupported failure", "adaptation-fidelity-review", func(in, out map[string]any) { out["verdict"] = "FAIL" }},
		{"self linked highlight", "adaptation-fidelity-review", func(in, out map[string]any) {
			out["checks"].([]any)[4].(map[string]any)["setup_payoff_link"].(map[string]any)["payoff_requirement_id"] = "req-5"
		}},
		{"pass on excerpt", "adaptation-fidelity-review", func(in, out map[string]any) {
			out["verdict"] = "PASS"
			out["limitations"] = []any{}
			for _, c := range out["checks"].([]any) {
				c.(map[string]any)["result"] = "PRESERVED"
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, out := memoryExample(t, tc.preset)
			if err := checkMemoryExample(tc.preset, in, out); err != nil {
				t.Fatalf("valid example: %v", err)
			}
			tc.edit(in, out)
			if err := checkMemoryExample(tc.preset, in, out); err == nil {
				t.Fatal("accepted invalid source or unsupported conclusion")
			}
		})
	}
}

func TestMemoryRuneAnchorsUseCodePointsAndPreserveRawText(t *testing.T) {
	in, out := memoryExample(t, "memory-consolidation")
	source := first(in, "sources")
	source["text"] = "😀门开着。"
	digest := sha256.Sum256([]byte(source["text"].(string)))
	source["hash"] = hex.EncodeToString(digest[:])
	in["sources"] = []any{source}
	candidate := first(out, "candidates")
	candidate["evidence"] = []any{map[string]any{"resource_id": source["resource_id"], "revision": source["revision"], "hash": source["hash"], "start_rune": 1, "end_rune": 4, "quote": "门开着"}}
	out["candidates"] = []any{candidate}
	out["findings"] = []any{}
	if err := checkMemoryExample("memory-consolidation", in, out); err != nil {
		t.Fatal(err)
	}
	first(candidate, "evidence")["start_rune"] = 2
	first(candidate, "evidence")["end_rune"] = 5
	if err := checkMemoryExample("memory-consolidation", in, out); err == nil {
		t.Fatal("accepted UTF-16 offsets as rune offsets")
	}
}

func TestCompleteFidelitySupportsBoundedPassAndEvidenceBackedFailure(t *testing.T) {
	in, out := memoryExample(t, "adaptation-fidelity-review")
	in["target_complete"] = true
	out["verdict"] = "PASS"
	out["limitations"] = []any{}
	for _, check := range out["checks"].([]any) {
		check.(map[string]any)["result"] = "PRESERVED"
	}
	// This proves contract acceptance only, not correctness of generated prose.
	if err := checkMemoryExample("adaptation-fidelity-review", in, out); err != nil {
		t.Fatal(err)
	}
	out["verdict"] = "FAIL"
	first(out, "checks")["result"] = "MISSING"
	if err := checkMemoryExample("adaptation-fidelity-review", in, out); err != nil {
		t.Fatal(err)
	}
}

func memoryExample(t *testing.T, name string) (map[string]any, map[string]any) {
	t.Helper()
	bundle, err := Bundle(name)
	if err != nil {
		t.Fatal(err)
	}
	var examples map[string]any
	if err := json.Unmarshal(bundle.Find("examples.json"), &examples); err != nil {
		t.Fatal(err)
	}
	return examples["input"].(map[string]any), examples["output"].(map[string]any)
}

func first(v map[string]any, key string) map[string]any { return v[key].([]any)[0].(map[string]any) }

func checkMemoryExample(name string, in, out map[string]any) error {
	input, err := json.Marshal(in)
	if err != nil {
		return err
	}
	output, err := json.Marshal(out)
	if err != nil {
		return err
	}
	return ValidateMemoryProposal(name, input, output)
}
