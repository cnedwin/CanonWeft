package novel

import (
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"reflect"
	"testing"
)

func TestCharacterDesignProposalPreservesLockedAndUnrequested(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "existing", Name: "林舟", Kind: "character"}}, Assertions: []domain.Assertion{}}
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), IDs: []string{"new"}, Base: base, Candidate: domain.Snapshot{Entities: append(append([]domain.Entity{}, base.Entities...), domain.Entity{ID: "new", Kind: "character", Name: "待完善"}), Assertions: []domain.Assertion{}}}
	p := CharacterProposal{Entities: []domain.Entity{{ID: "new", Kind: "character", Name: "阿岚"}}, Assertions: []domain.Assertion{{ID: "occupation", Subject: "new", Predicate: "职业", Value: "医师", Kind: "fact", Source: "候选"}}}
	raw, _ := json.Marshal(p)
	next, _, err := ApplyCharacterProposal(d, raw, "batch", "generation")
	if err != nil || len(next.Candidate.Entities) != 2 || next.Candidate.Entities[0].Name != "林舟" || next.Candidate.Assertions[0].Source != "character-design:batch:generation:generation" {
		t.Fatal("proposal lost scope/provenance", err)
	}
	next.Candidate.Assertions[0].Locked = true
	changed := p
	changed.Assertions = append([]domain.Assertion{}, next.Candidate.Assertions...)
	changed.Assertions[0].Value = "改坏锁定职业"
	raw, _ = json.Marshal(changed)
	if _, _, err = ApplyCharacterProposal(next, raw, "batch", "second"); err == nil {
		t.Fatal("model overwrote candidate lock")
	}
	p.Entities[0].ID = "existing"
	raw, _ = json.Marshal(p)
	if _, _, err = ApplyCharacterProposal(d, raw, "batch", "bad"); err == nil {
		t.Fatal("model replaced unrequested character")
	}
}

func TestCharacterDesignSelectionLocksAndReplay(t *testing.T) {
	a, b := CharacterDesignID("batch", "person", "1"), CharacterDesignID("batch", "person", "2")
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "old", Name: "林舟", Kind: "character", Locked: true}}, Assertions: []domain.Assertion{}}
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), IDs: []string{a, b}, Base: base, Candidate: domain.Snapshot{Entities: append([]domain.Entity{}, base.Entities...), Assertions: []domain.Assertion{}}}
	d.Candidate.Entities = append(d.Candidate.Entities, domain.Entity{ID: a, Name: "林舟", Kind: "character"}, domain.Entity{ID: b, Name: "阿岚", Kind: "character"})
	d.Candidate.Assertions = append(d.Candidate.Assertions, domain.Assertion{ID: "relation", Subject: a, Predicate: "同门", Value: "师妹", ObjectID: b, Kind: "fact", Source: "作者待确认", Visibility: "author"})
	if _, _, err := MergeCharacterDesign(d, base, []string{a}); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("dangling relation accepted", err)
	}
	merged, warnings, err := MergeCharacterDesign(d, base, []string{a, b})
	if err != nil || len(merged.Entities) != 3 || len(warnings) == 0 {
		t.Fatal("same names merged or unreported", err, warnings)
	}
	replayed, _, err := MergeCharacterDesign(d, merged, []string{a, b})
	if err != nil || !reflect.DeepEqual(merged, replayed) {
		t.Fatal("replay duplicated characters", err)
	}
	d.Candidate.Entities[0].Name = "擅改锁定人名"
	if _, _, err := MergeCharacterDesign(d, base, []string{a, b}); err == nil {
		t.Fatal("unrelated locked character overwritten")
	}
}

func TestCharacterDesignPreservesConcurrentEdits(t *testing.T) {
	id := CharacterDesignID("batch", "person", "1")
	base := domain.Snapshot{Entities: []domain.Entity{{ID: id, Name: "阿岚", Kind: "character"}}, Assertions: []domain.Assertion{{ID: "f", Subject: id, Predicate: "职业", Value: "医师", Kind: "fact", Source: "作者", Locked: false}}}
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), IDs: []string{id}, Base: base, Candidate: domain.Snapshot{Entities: append([]domain.Entity{}, base.Entities...), Assertions: append([]domain.Assertion{}, base.Assertions...)}}
	d.Candidate.Assertions[0].Value = "游医"
	current := domain.Snapshot{Entities: base.Entities, Assertions: append([]domain.Assertion{}, base.Assertions...)}
	current.Assertions[0].Value = "作者刚手改的御医"
	if _, _, err := MergeCharacterDesign(d, current, []string{id}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("hand edit overwritten", err)
	}
	base.Assertions[0].Locked = true
	d.Base = base
	if err := ValidateCharacterDesign(d); err == nil {
		t.Fatal("locked field changed")
	}
}

func TestCharacterDesignPartialEnrichmentCanContinue(t *testing.T) {
	base := domain.Snapshot{Entities: []domain.Entity{{ID: "a", Name: "甲", Kind: "character"}, {ID: "b", Name: "乙", Kind: "character"}}, Assertions: []domain.Assertion{}}
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), IDs: []string{"a", "b"}, Base: base, Candidate: domain.Snapshot{Entities: append([]domain.Entity{}, base.Entities...), Assertions: []domain.Assertion{}}}
	d.Candidate.Entities[0].Description = "甲的新背景"
	d.Candidate.Entities[1].Description = "乙的新背景"
	first, _, err := MergeCharacterDesign(d, base, []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := MergeCharacterDesign(d, first, []string{"b"})
	if err != nil || !reflect.DeepEqual(second, d.Candidate) {
		t.Fatal("second selected enrichment blocked", err)
	}
}
