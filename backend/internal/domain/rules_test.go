package domain

import (
	"testing"
	"time"
)

func TestRuleEvaluationDistinguishesUnknownAndViolation(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "hero", Name: "主角", Kind: "character"}}, Rules: []WorldRule{{ID: "limit", Name: "修炼上限", Subject: "hero", Predicate: "境界", Operator: "number_lte", Operand: "9", Source: "作者"}}}
	results := EvaluateRules(s, 0)
	if len(results) != 1 || results[0].Status != "UNKNOWN" {
		t.Fatal("missing evidence is not success", results)
	}
	s.Assertions = []Assertion{{ID: "level", Subject: "hero", Predicate: "境界", Value: "10", Kind: "fact", Source: "作者"}}
	if EvaluateRules(s, 0)[0].Status != "CONFLICT" {
		t.Fatal("rule violation passed")
	}
	s.Assertions[0].Value = "8"
	if EvaluateRules(s, 0)[0].Status != "SATISFIED" {
		t.Fatal("valid level rejected")
	}
	s.Assertions[0].Kind = "belief"
	if EvaluateRules(s, 0)[0].Status != "UNKNOWN" {
		t.Fatal("belief used as canonical evidence")
	}
}
func TestEventReferencesAndCausality(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "hero", Name: "主角", Kind: "character"}}, Events: []StoryEvent{{ID: "escape", Name: "逃离", Time: 2, Participants: []string{"hero"}, Causes: []string{"alarm"}, Source: "作者"}}}
	if ValidateSnapshot(s) == nil {
		t.Fatal("dangling cause accepted")
	}
	s.Events = append(s.Events, StoryEvent{ID: "alarm", Name: "警报", Time: 3, Source: "作者"})
	if ValidateSnapshot(s) == nil {
		t.Fatal("future event caused earlier event")
	}
	s.Events[1].Time = 1
	if err := ValidateSnapshot(s); err != nil {
		t.Fatal(err)
	}
	s.Events[1].Causes = []string{"escape"}
	if ValidateSnapshot(s) == nil {
		t.Fatal("causal cycle accepted")
	}
}
func TestRecordedTimeIsServerOwned(t *testing.T) {
	base := Snapshot{Assertions: []Assertion{{ID: "f", Subject: "hero", Predicate: "身份", Value: "医师", Kind: "fact", RecordedAt: "2020-01-01T00:00:00Z", Locked: true}}}
	next := Snapshot{Assertions: append([]Assertion{}, base.Assertions...)}
	next.Assertions[0].RecordedAt = "2099-01-01T00:00:00Z"
	now := time.Date(2026, 10, 24, 1, 2, 3, 0, time.UTC)
	StampSnapshot(base, &next, now)
	if next.Assertions[0].RecordedAt != base.Assertions[0].RecordedAt {
		t.Fatal("client changed record timestamp")
	}
	if err := CheckLocked(base, next); err != nil {
		t.Fatal(err)
	}
	next.Assertions[0].Value = "刺客"
	StampSnapshot(base, &next, now)
	if next.Assertions[0].RecordedAt != now.Format(time.RFC3339Nano) {
		t.Fatal("changed fact not timestamped")
	}
}
