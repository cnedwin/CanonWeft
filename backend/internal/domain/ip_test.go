package domain

import "testing"

func fixture() Snapshot {
	return Snapshot{
		Entities:   []Entity{{ID: "a", Kind: "character", Name: "林舟"}, {ID: "b", Kind: "character", Name: "林舟"}},
		Assertions: []Assertion{{ID: "f", Subject: "a", Predicate: "身份", Value: "医师", Kind: "fact", EffectiveFrom: 1, KnownFrom: 3, KnownBy: []string{"a"}, Source: "作者确认"}},
	}
}

func TestLieIsNotFact(t *testing.T) {
	s := fixture()
	s.Assertions = append(s.Assertions, Assertion{ID: "lie", Subject: "b", Predicate: "身份", Value: "皇子", Kind: "claim", Source: "角色台词"})
	for _, fact := range KnownFacts(s, "b", 100) {
		if fact.ID == "lie" {
			t.Fatal("claim became fact")
		}
	}
}
func TestSameNameKeepsIdentity(t *testing.T) {
	s := fixture()
	s.Entities[1].ID = "a"
	if ValidateSnapshot(s) == nil {
		t.Fatal("duplicate identity accepted")
	}
	s = fixture()
	if ValidateSnapshot(s) != nil {
		t.Fatal("same names must not merge or reject identities")
	}
}
func TestFlashbackKnowledge(t *testing.T) {
	s := fixture()
	if len(KnownFacts(s, "a", 2)) != 0 {
		t.Fatal("future knowledge leaked into flashback")
	}
	if len(KnownFacts(s, "b", 5)) != 0 {
		t.Fatal("private knowledge leaked to other role")
	}
	if len(KnownFacts(s, "a", 5)) != 1 {
		t.Fatal("known fact disappeared")
	}
}
func TestLockedCanonChange(t *testing.T) {
	base, next := fixture(), fixture()
	base.Assertions[0].Locked = true
	next.Assertions[0].Value = "刺客"
	if CheckLocked(base, next) == nil {
		t.Fatal("locked identity was changed")
	}
	next.Assertions = nil
	if CheckLocked(base, next) == nil {
		t.Fatal("locked identity deleted")
	}
}
func TestBranchExceptionSurvivesMerge(t *testing.T) {
	base, upstream, local := fixture(), fixture(), fixture()
	local.Assertions[0].Value = "分支医师"
	upstream.Assertions = append(upstream.Assertions, Assertion{ID: "new", Subject: "b", Predicate: "职业", Value: "剑士", Kind: "fact", Source: "作者确认"})
	merged, conflicts := MergeSnapshots(base, upstream, local)
	if len(conflicts) > 0 {
		t.Fatal("unrelated upstream change conflicted")
	}
	found := false
	for _, a := range merged.Assertions {
		if a.ID == "f" && a.Value == "分支医师" {
			found = true
		}
	}
	if !found || len(merged.Assertions) != 2 {
		t.Fatal("local exception overwritten or upstream addition lost")
	}
	upstream.Assertions[0].Value = "皇帝"
	_, conflicts = MergeSnapshots(base, upstream, local)
	if len(conflicts) == 0 {
		t.Fatal("two-sided conflict silently resolved")
	}
}
func TestCompositeDoesNotMergeBeliefs(t *testing.T) {
	s := fixture()
	s.Assertions[0].Kind = "belief"
	s.Assertions[0].Subject = "b"
	if len(KnownFacts(s, "a", 100)) != 0 {
		t.Fatal("belief became shared authoritative fact")
	}
}
