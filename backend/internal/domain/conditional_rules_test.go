package domain

import "testing"

func TestConditionalRulesRespectWindowsAndExplicitExceptions(t *testing.T) {
	start, end := int64(2), int64(10)
	s := Snapshot{Entities: []Entity{{ID: "hero", Kind: "character", Name: "主角"}}, Assertions: []Assertion{
		{ID: "power", Subject: "hero", Predicate: "力量", Value: "20", Kind: "fact", Visibility: "public", Source: "设定"},
		{ID: "place", Subject: "hero", Predicate: "所在", Value: "城内", Kind: "fact", Source: "设定"},
		{ID: "permit", Subject: "hero", Predicate: "许可", Value: "有", Kind: "fact", EffectiveFrom: 4, Source: "设定"},
	}, Rules: []WorldRule{{ID: "r", Name: "城内力量上限", Subject: "hero", Predicate: "力量", Operator: "number_lte", Operand: "10", Source: "法则", EffectiveFrom: &start, EffectiveUntil: &end,
		When:       []PredicateTest{{Subject: "hero", Predicate: "所在", Operator: "equals", Operand: "城内"}},
		Exceptions: []RuleException{{ID: "permit-exception", Name: "许可豁免", Source: "例外设定", When: []PredicateTest{{Subject: "hero", Predicate: "许可", Operator: "equals", Operand: "有"}}}},
	}}}
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	for at, want := range map[int64]string{1: "NOT_APPLICABLE", 3: "UNKNOWN", 4: "EXEMPT", 10: "NOT_APPLICABLE"} {
		if got := EvaluateRules(s, at)[0]; got.Status != want {
			t.Fatalf("time %d: %+v want %s", at, got, want)
		}
	}
	s.Assertions[2].Value = "无"
	if got := EvaluateRules(s, 4)[0]; got.Status != "CONFLICT" {
		t.Fatal("false exception bypassed rule", got)
	}
	s.Assertions[1].Value = "城外"
	if got := EvaluateRules(s, 4)[0]; got.Status != "NOT_APPLICABLE" {
		t.Fatal("scope mismatch applied rule", got)
	}
}

func TestRuleReviewVisitsConditionChangesAndExpiry(t *testing.T) {
	until := int64(3)
	s := Snapshot{Assertions: []Assertion{
		{ID: "power", Subject: "hero", Predicate: "力量", Value: "20", Kind: "fact"},
		{ID: "outside", Subject: "hero", Predicate: "所在", Value: "城外", Kind: "fact", EffectiveUntil: &until},
		{ID: "inside", Subject: "hero", Predicate: "所在", Value: "城内", Kind: "fact", EffectiveFrom: 3},
	}, Rules: []WorldRule{{ID: "limit", Subject: "hero", Predicate: "力量", Operator: "number_lte", Operand: "10", When: []PredicateTest{{Subject: "hero", Predicate: "所在", Operator: "equals", Operand: "城内"}}}}}
	conflict := false
	for _, result := range ReviewRules(s) {
		if result.StoryTime == 3 && result.Status == "CONFLICT" {
			conflict = true
		}
	}
	if !conflict {
		t.Fatal("review skipped conditional transition")
	}
}
