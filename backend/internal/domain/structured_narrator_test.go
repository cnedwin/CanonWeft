package domain

import "testing"

func TestStructuredNarratorKeepsHiddenKnowledgeSeparate(t *testing.T) {
	snapshot := Snapshot{Entities: []Entity{{ID: "hero", Name: "林舟"}}, Assertions: []Assertion{
		{ID: "public", Subject: "hero", Predicate: "职业", Value: "医者", Kind: "fact", Visibility: "public"},
		{ID: "secret", Subject: "hero", Predicate: "秘密", Value: "师父藏身旧城", Kind: "fact", Visibility: "restricted", KnownBy: []string{"hero"}},
		{ID: "future", Subject: "hero", Predicate: "未来", Value: "即将找到师父", Kind: "fact", Visibility: "public", EffectiveFrom: 20, KnownFrom: 20},
	}}
	req := StructuredRequest{Purpose: "writer", Query: "林舟", StoryTime: 1, OptionalBytes: 10000}
	p, err := SelectStructured(snapshot, req)
	if err != nil || len(p.Facts) != 1 || p.Facts[0].ID != "public" {
		t.Fatal("narrator leaked restricted/future knowledge or could not write", p, err)
	}
	if len(p.Hidden) != 3 {
		t.Fatal("narrator lost author constraints", p)
	}
	req.Purpose = "npc"
	if _, err := SelectStructured(snapshot, req); err == nil {
		t.Fatal("NPC accepted missing identity")
	}
	req.Purpose, req.Perspective = "writer", "missing"
	if _, err := SelectStructured(snapshot, req); err == nil {
		t.Fatal("writer accepted nonexistent selected identity")
	}
	req.Perspective = ""
	if _, err := SelectStructured(Snapshot{}, req); err != nil {
		t.Fatal("new book cannot write without published people", err)
	}
}
