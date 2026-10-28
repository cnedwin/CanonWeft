package domain

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func largeStructuredFixture() Snapshot {
	s := Snapshot{Entities: []Entity{{ID: "hero", Name: "林舟", Aliases: []string{"青衣客"}}, {ID: "friend", Name: "伙伴"}, {ID: "core", Name: "主题承诺", Locked: true}},
		Assertions: []Assertion{{ID: "core-fact", Subject: "core", Predicate: "底线", Value: "绝不牺牲无辜", Kind: "fact", Visibility: "author", Locked: true},
			{ID: "relation", Subject: "hero", ObjectID: "friend", Predicate: "伙伴", Value: "共同寻找失踪者", Kind: "fact", Visibility: "public"}}}
	for i := 0; i < 1500; i++ {
		id := fmt.Sprintf("extra-%04d", i)
		s.Entities = append(s.Entities, Entity{ID: id, Name: fmt.Sprintf("配角%d", i)})
		s.Assertions = append(s.Assertions, Assertion{ID: id, Subject: id, Predicate: "前史", Value: strings.Repeat("远方城镇的历史细节。", 12), Kind: "fact", Visibility: "public"})
	}
	return s
}

func TestStructuredContextBoundsLongBookAndKeepsIdentityClosure(t *testing.T) {
	s := largeStructuredFixture()
	req := StructuredRequest{Purpose: "writer", Perspective: "hero", Query: "青衣客寻找失踪者", StoryTime: 5, OptionalBytes: 5000}
	p, err := SelectStructured(s, req)
	if err != nil {
		t.Fatal(err)
	}
	if p.Selection.OmittedAssertions < 1400 || len(p.Facts) > 20 {
		t.Fatal("unbounded longbook", p.Selection)
	}
	facts := map[string]bool{}
	entities := map[string]bool{}
	for _, f := range p.Hidden {
		facts[f.ID] = true
	}
	for _, e := range p.Entities {
		entities[e.ID] = true
	}
	if !facts["core-fact"] || !facts["relation"] || !entities["core"] || !entities["hero"] || !entities["friend"] {
		t.Fatal("lost anchor/relation identity", p)
	}
	a, _ := json.Marshal(p)
	again, err := SelectStructured(s, req)
	b, _ := json.Marshal(again)
	if err != nil || string(a) != string(b) {
		t.Fatal("unstable frozen selection", err)
	}
	if len(s.Assertions) != 1502 {
		t.Fatal("selection mutated canon")
	}
}

func TestStructuredContextNPCDoesNotRevealSecretMembership(t *testing.T) {
	s := largeStructuredFixture()
	req := StructuredRequest{Purpose: "npc", Perspective: "hero", Query: "寻找伙伴", StoryTime: 5, OptionalBytes: 3000}
	before, err := SelectStructured(s, req)
	if err != nil {
		t.Fatal(err)
	}
	s.Entities = append(s.Entities, Entity{ID: "secret-entity", Name: "隐秘身份", Locked: true})
	s.Assertions = append(s.Assertions, Assertion{ID: "secret", Subject: "secret-entity", Predicate: "隐藏", Value: "密室入口", Kind: "fact", Visibility: "author", Locked: true})
	after, err := SelectStructured(s, req)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) || len(after.Hidden) != 0 || len(after.Entities) != 0 || len(after.Rules) != 0 {
		t.Fatal("hidden membership leaked")
	}
	req.Scope = &StructuredScope{RequiredAssertionIDs: []string{"secret"}}
	if _, err = SelectStructured(s, req); err != ErrForbidden {
		t.Fatal("invisible required fact accepted", err)
	}
}

func TestStructuredContextRequiredProfileClosureAndNoTruncation(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "hero", Name: "主角", Profile: &CapabilityProfile{Related: []string{"skill"}, Requirements: []PredicateTest{{Subject: "hero", Predicate: "体力"}}, Source: "设定", Effects: []string{"施法"}}}, {ID: "skill", Name: "法术"}},
		Assertions: []Assertion{{ID: "energy-old", Subject: "hero", Predicate: "体力", Value: "10", Kind: "fact", Visibility: "public"},
			{ID: "energy", Subject: "hero", Predicate: "体力", Value: "5", Kind: "fact", Visibility: "public", EffectiveFrom: 2},
			{ID: "needed", Subject: "skill", Predicate: "代价", Value: strings.Repeat("绝不能省略的代价。", 100), Kind: "fact", Visibility: "author"}}}
	req := StructuredRequest{Purpose: "author", StoryTime: 3, Scope: &StructuredScope{FocusEntityIDs: []string{"hero"}, RequiredAssertionIDs: []string{"needed"}}}
	p, err := SelectStructured(s, req)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, a := range p.Facts {
		ids[a.ID] = true
		if a.ID == "needed" && a.Value != s.Assertions[2].Value {
			t.Fatal("required text truncated")
		}
	}
	if !ids["energy"] || !ids["needed"] || ids["energy-old"] || len(p.Entities) != 2 {
		t.Fatal("profile prerequisite closure lost", p)
	}
	req.Scope.FocusEntityIDs = []string{"hero", "hero"}
	if _, err = SelectStructured(s, req); err != ErrValidation {
		t.Fatal("duplicate focus accepted", err)
	}
}

func TestStructuredContextPrefersNamedEntityWithoutAssertions(t *testing.T) {
	s := largeStructuredFixture()
	s.Entities = append(s.Entities, Entity{ID: "visitor", Name: "访客", Aliases: []string{"白衣人"}})
	p, err := SelectStructured(s, StructuredRequest{Purpose: "author", Query: "白衣人", OptionalBytes: 1000})
	if err != nil {
		t.Fatal(err)
	}
	for _, entity := range p.Entities {
		if entity.ID == "visitor" {
			return
		}
	}
	t.Fatal("unrelated history crowded out explicitly named identity")
}

func BenchmarkStructuredContextLongBook(b *testing.B) {
	s := largeStructuredFixture()
	for i := range s.Assertions {
		s.Assertions[i].Value = strings.Repeat("远方城镇的历史细节。", 22)
	}
	req := StructuredRequest{Purpose: "writer", Perspective: "hero", Query: "青衣客寻找失踪者", StoryTime: 5, OptionalBytes: 10000}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := SelectStructured(s, req); err != nil {
			b.Fatal(err)
		}
	}
}

func TestStructuredContextKeepsKnowingCharacterIdentity(t *testing.T) {
	s := Snapshot{Entities: []Entity{{ID: "hero"}, {ID: "witness", Profile: &CapabilityProfile{Related: []string{"skill"}}}, {ID: "skill"}}, Assertions: []Assertion{{ID: "a", Subject: "hero", KnownBy: []string{"witness"}, Kind: "fact", Value: "线索", Visibility: "restricted"}}}
	p, err := SelectStructured(s, StructuredRequest{Purpose: "author", Scope: &StructuredScope{RequiredAssertionIDs: []string{"a"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Entities) != 3 || p.Selection.RequiredEntities != 3 {
		t.Fatal("knowing character identity/profile lost", p.Entities)
	}
}

func TestStructuredOptionalClosureStopsAtBudget(t *testing.T) {
	b := structureBuilder{entities: map[string]Entity{"first": {ID: "first", Profile: &CapabilityProfile{Related: []string{"missing"}}}}, selected: newStructureDelta()}
	// A failed optional group must stop before traversing a huge dependency graph,
	// and must not mutate the selected hard-constraint set.
	_, err := b.closureWithin(nil, []string{"first"}, 1)
	if err != ErrContextBudget || len(b.selected.entities) != 0 {
		t.Fatal("optional traversal did not stop", err)
	}
	if _, err = b.closure(nil, []string{"first"}); err != ErrValidation {
		t.Fatal("mandatory traversal was shortened", err)
	}
}
