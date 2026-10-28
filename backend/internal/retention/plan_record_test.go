package retention

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestStoredPlanPreservesBuilderHashAndRejectsRewrittenIntent(t *testing.T) {
	inv, now := fixtureInventory()
	inv.References = []Reference{fixtureRef("1", "model_calls.output_hash", strings.Repeat("b", 64), now.Add(-31*24*time.Hour))}
	plan, err := BuildPlan(inv, now)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(plan)
	if _, err := ValidatePlanRecord(string(raw)); err != nil {
		t.Fatal("builder plan rejected", err)
	}
	for _, change := range []func(*Plan){
		func(p *Plan) { p.InventoryHash = "bad" },
		func(p *Plan) { p.Expire = append(p.Expire, p.Expire[0]) },
		func(p *Plan) { p.Expire[0].Storage = Inline },
		func(p *Plan) { p.Expire[0].Rule = "missing.column" },
		func(p *Plan) { p.Expire[0].Deadline = now.Add(time.Second) },
		func(p *Plan) { p.DeleteObjects = append(p.DeleteObjects, p.DeleteObjects[0]) },
		func(p *Plan) { p.Blocked = []string{p.Expire[0].ReferenceID} },
		func(p *Plan) { p.Blocked = []string{"object:" + p.DeleteObjects[0]} },
		func(p *Plan) { p.Blocked = []string{"../../private"} },
	} {
		var mutated Plan
		if err := json.Unmarshal(raw, &mutated); err != nil {
			t.Fatal(err)
		}
		change(&mutated)
		mutated.Hash = ""
		mutated.Hash = stableHash(mutated)
		candidate, _ := json.Marshal(mutated)
		if _, err := ValidatePlanRecord(string(candidate)); err == nil {
			t.Fatal("rehashed malformed intent accepted", string(candidate))
		}
	}
	for _, candidate := range []string{string(raw) + " ", strings.Replace(string(raw), `"overdue":false`, `"overdue":true`, 1), strings.Replace(string(raw), `"overdue":false`, `"overdue":false,"overdue":false`, 1)} {
		if _, err := ValidatePlanRecord(candidate); err == nil {
			t.Fatal("noncanonical or altered plan accepted")
		}
	}
}
