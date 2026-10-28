package novel

import (
	"encoding/json"
	"novelstudio/internal/domain"
	"reflect"
	"testing"
)

func TestNamingBoardPreservesLockedCandidates(t *testing.T) {
	// The persisted design contract must retain the naming board, not silently
	// discard it when the author saves another character field.
	raw := []byte(`{"schema":"character-design-v1","naming":{"count":20,"culture":"架空","era":"古代","candidates":[{"id":"kept","name":"林川","locked":true}]}}`)
	var d CharacterDesign
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	json.Unmarshal(out, &result)
	if _, ok := result["naming"]; !ok {
		t.Fatal("naming board was discarded by character draft persistence")
	}
}

func TestNamingMergeLocksEvidenceAndCollision(t *testing.T) {
	kept := NameCandidate{ID: "kept", Name: "林川", Pronunciation: "Lin Chuan", Origin: "invented", Confidence: "unverified", Locked: true}
	board := NamingBoard{Count: 3, Language: "zh", NameProposal: NameProposal{Candidates: []NameCandidate{kept}}}
	p := NameProposal{Candidates: []NameCandidate{
		{Name: " 林 川 ", Pronunciation: "Lin", Origin: "invented", Confidence: "unverified"},
		{Name: "林舟", Pronunciation: "Lin Zhou", Origin: "invented", Confidence: "unverified"},
		{Name: "顾行", Pronunciation: "Gu Xing", Origin: "invented", Confidence: "unverified"},
	}}
	next, err := MergeNameProposal(board, p, "generation", []domain.Entity{{ID: "old", Name: "林舟"}}, nil)
	if err != nil || len(next.Candidates) != 3 || !reflect.DeepEqual(next.Candidates[0], kept) || len(next.Candidates[1].Conflicts) == 0 {
		t.Fatalf("lock/dedup/collision: %#v %v", next, err)
	}
	if len(board.Candidates) != 1 || board.GenerationID != "" {
		t.Fatal("input mutated")
	}
	p.Candidates[0].Sources = []NameSource{{SourceID: "fake", URL: "https://invalid.example", Evidence: "invented"}}
	if _, err = MergeNameProposal(board, p, "forged", nil, nil); err == nil {
		t.Fatal("fabricated source accepted")
	}
	p.Candidates[0].Sources = nil
	p.Candidates[0].Confidence = "supported"
	if _, err = MergeNameProposal(board, p, "unsupported", nil, nil); err == nil {
		t.Fatal("unsupported provenance accepted")
	}
}

func TestNamingCharacterRenamePreservesIdentityAndAlias(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Name: "林舟", Kind: "character"}}}
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), IDs: []string{"hero"}, Base: base, Candidate: domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Name: "林川", Kind: "character"}}}}
	got, _, err := MergeCharacterDesign(d, base, []string{"hero"})
	if err != nil || got.Entities[0].ID != "hero" || got.Entities[0].Name != "林川" || !reflect.DeepEqual(got.Entities[0].Aliases, []string{"林舟"}) {
		t.Fatal("rename lost stable identity or former name", got, err)
	}
}
