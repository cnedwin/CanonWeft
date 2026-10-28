package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestContinuityRepairAnchorsScopeAndCoverage(t *testing.T) {
	id, rev, branch, planID := strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32), strings.Repeat("d", 32)
	ref := DependencyRef{Kind: "resource", ID: id, Revision: rev, BranchID: branch}
	source := ChangeReviewContext{Plan: ChangePlan{ID: planID, BranchID: branch, Items: []ImpactItem{{Node: ref}, {Node: DependencyRef{Kind: "resource", ID: strings.Repeat("e", 32), BranchID: branch}}}}, Targets: []ChangeReviewTarget{{Ref: ref, Text: "她说：🌙旧名来了。", BodyHash: patchHash("她说：🌙旧名来了。")}}}
	base := map[string]any{"schema": 1, "status": "CANDIDATE", "plan_id": planID, "summary": "仅修订称呼，其他影响待核对。", "patches": []any{map[string]any{"resource_id": id, "base_revision": rev, "base_hash": source.Targets[0].BodyHash, "start_rune": 4, "end_rune": 6, "expected_text": "旧名", "replacement": "林舟", "reason": "与新角色设定一致"}}}
	encode := func(v any) string { raw, _ := json.Marshal(v); return string(raw) }
	good := encode(base)
	result, err := CompileContinuityRepair(good, strings.Repeat("f", 32), source)
	if err != nil || len(result.Items) != 1 || result.Items[0].Result.Text != "她说：🌙林舟来了。" || len(result.Unaddressed) != 1 {
		t.Fatalf("unexpected repair: %+v %v", result, err)
	}
	again, err := CompileContinuityRepair(good, strings.Repeat("f", 32), source)
	if err != nil || encode(again.Group) != encode(result.Group) {
		t.Fatal("repair identity is not deterministic", err)
	}
	for name, change := range map[string]func(map[string]any){
		"wrong-plan":     func(v map[string]any) { v["plan_id"] = id },
		"unknown-field":  func(v map[string]any) { v["approved"] = true },
		"bad-anchor":     func(v map[string]any) { v["patches"].([]any)[0].(map[string]any)["expected_text"] = "别名" },
		"wrong-version":  func(v map[string]any) { v["patches"].([]any)[0].(map[string]any)["base_revision"] = id },
		"outside-plan":   func(v map[string]any) { v["patches"].([]any)[0].(map[string]any)["resource_id"] = planID },
		"overlap":        func(v map[string]any) { p := v["patches"].([]any); v["patches"] = append(p, p[0]) },
		"missing-offset": func(v map[string]any) { delete(v["patches"].([]any)[0].(map[string]any), "start_rune") },
	} {
		t.Run(name, func(t *testing.T) {
			var v map[string]any
			_ = json.Unmarshal([]byte(good), &v)
			change(v)
			if _, err := CompileContinuityRepair(encode(v), strings.Repeat("f", 32), source); err == nil {
				t.Fatal("unsafe output accepted")
			}
		})
	}
	other := source
	other.Targets = append([]ChangeReviewTarget(nil), source.Targets...)
	other.Targets[0].Ref.BranchID = strings.Repeat("e", 32)
	if _, err := CompileContinuityRepair(good, strings.Repeat("f", 32), other); err == nil {
		t.Fatal("cross-branch edit accepted")
	}
	base["patches"] = []any{}
	empty, err := CompileContinuityRepair(encode(base), strings.Repeat("f", 32), source)
	if err != nil || len(empty.Items) != 0 || len(empty.Unaddressed) != 2 {
		t.Fatal("empty output claimed coverage", err)
	}
}
