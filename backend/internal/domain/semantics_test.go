package domain

import "testing"

func TestTypedCompositeNeverTransfersSecretsOrBeliefs(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "a", Name: "阿岚", Kind: "character"}, {ID: "b", Name: "白羽", Kind: "character"}, {ID: "c", Name: "合成角色", Kind: "character"}}, Assertions: []Assertion{{ID: "secret", Subject: "a", Predicate: "身份", Value: "密探", Kind: "fact", Visibility: "restricted", KnownBy: []string{"a"}, Source: "作者"}, {ID: "belief", Subject: "b", HolderID: "a", Predicate: "身份", Value: "商人", Kind: "belief", Source: "角色判断"}}, Mappings: []EntityMapping{{ID: "m", Kind: "composite_of", Target: "c", Sources: []string{"a", "b"}, Reason: "将两条配角支线合并", Source: "改编计划"}}}
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	if got := KnownFacts(s, "c", 1); len(got) != 0 {
		t.Fatal("mapping inherited secret", got)
	}
	s.Mappings[0].Sources = []string{"c"}
	if err := ValidateSnapshot(s); err == nil {
		t.Fatal("self composite accepted")
	}
}

func TestDreamAndRetractedAssertionsCannotSatisfyWorldRule(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "a", Name: "角色", Kind: "character"}}, Assertions: []Assertion{{ID: "dream", Subject: "a", Predicate: "年龄", Value: "300", Kind: "fact", Context: "dream", Visibility: "public", Source: "梦境"}}, Rules: []WorldRule{{ID: "age", Name: "年龄限制", Subject: "a", Predicate: "年龄", Operator: "number_lte", Operand: "100", Source: "世界设定"}}}
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	if len(KnownFacts(s, "a", 0)) != 0 || EvaluateRules(s, 0)[0].Status != "UNKNOWN" {
		t.Fatal("dream became authority")
	}
	s.Assertions[0].Context = "reality"
	s.Assertions[0].Status = "retracted"
	if len(KnownFacts(s, "a", 0)) != 0 || EvaluateRules(s, 0)[0].Status != "UNKNOWN" {
		t.Fatal("retracted fact became authority")
	}
	s.Assertions[0].Status = "active"
	s.Assertions[0].Context = "flashback"
	if len(KnownFacts(s, "a", 0)) != 1 {
		t.Fatal("flashback reality lost authority")
	}
}

func TestStructuredAbilityRequiresEvidenceAndHasUnknownState(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "caster", Name: "施术者", Kind: "character"}, {ID: "fire", Name: "借火术", Kind: "ability", Profile: &CapabilityProfile{Requirements: []PredicateTest{{Subject: "caster", Predicate: "接触火源", Operator: "equals", Operand: "是"}}, Effects: []string{"转移外部火焰"}, Costs: []string{"消耗体力"}, Limits: []string{"不能凭空生火"}, Counters: []string{"隔绝火源"}, Growth: []string{"提高可转移距离"}, Source: "世界设定"}}}}
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	if result := EvaluateCapability(s, "fire", 0); result.Status != "UNKNOWN" {
		t.Fatal(result)
	}
	s.Assertions = []Assertion{{ID: "touch", Subject: "caster", Predicate: "接触火源", Value: "是", Kind: "fact", Visibility: "public", Source: "场景证据"}}
	if result := EvaluateCapability(s, "fire", 0); result.Status != "SATISFIED" {
		t.Fatal(result)
	}
	s.Assertions[0].Value = "否"
	if result := EvaluateCapability(s, "fire", 0); result.Status != "CONFLICT" {
		t.Fatal(result)
	}
}
