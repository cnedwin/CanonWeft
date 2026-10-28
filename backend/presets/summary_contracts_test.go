package presets

import "testing"

func TestSummaryProposalRequiresTargetCoverageAndPlanSemantics(t *testing.T) {
	for _, change := range []string{"valid", "wrong-level", "wrong-kind", "missing-source", "plan-as-fact", "backdate"} {
		t.Run(change, func(t *testing.T) {
			in, out := memoryExample(t, "memory-consolidation")
			in["summary_target"] = map[string]any{"level": "chapter", "unit_id": "unit-1", "title": "第一章", "timeline": "main"}
			candidate := first(out, "candidates")
			candidate["kind"], candidate["summary_level"], candidate["context"] = "summary", "chapter", "reality"
			out["candidates"] = []any{candidate}
			out["findings"] = []any{}
			in["sources"] = []any{first(in, "sources")}
			switch change {
			case "wrong-level":
				candidate["summary_level"] = "volume"
			case "wrong-kind":
				candidate["kind"] = "fact"
			case "missing-source":
				extra := map[string]any{}
				for k, v := range first(in, "sources") {
					extra[k] = v
				}
				extra["resource_id"] = "another-source"
				in["sources"] = append(in["sources"].([]any), extra)
			case "plan-as-fact":
				first(in, "sources")["control"] = true
			case "backdate":
				candidate["scope"].(map[string]any)["story_time"] = 0
			}
			err := checkMemoryExample("memory-consolidation", in, out)
			if (err == nil) != (change == "valid") {
				t.Fatalf("%s: %v", change, err)
			}
		})
	}
}
