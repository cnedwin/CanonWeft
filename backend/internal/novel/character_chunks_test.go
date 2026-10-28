package novel

import (
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"testing"
)

func TestCharacterChunksCoverageAndScopedMerge(t *testing.T) {
	d := CharacterDesign{Schema: "character-design-v1", BookID: CharacterDesignID("book", "id", "1"), Base: domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}, Candidate: domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}}
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("person-%d", i)
		d.IDs = append(d.IDs, id)
		d.Candidate.Entities = append(d.Candidate.Entities, domain.Entity{ID: id, Kind: "character", Name: "待完善"})
	}
	chunks, err := CharacterChunks(d.IDs, 5)
	if err != nil || len(chunks) != 3 || len(chunks[2]) != 1 {
		t.Fatal("coverage", chunks, err)
	}
	for i, ids := range chunks {
		p := CharacterProposal{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
		for _, id := range ids {
			p.Entities = append(p.Entities, domain.Entity{ID: id, Kind: "character", Name: id})
			p.Assertions = append(p.Assertions, domain.Assertion{ID: "occupation", Subject: id, Predicate: "职业", Value: fmt.Sprint(i), Kind: "fact", Source: "候选"})
		}
		raw, _ := json.Marshal(p)
		d, _, err = ApplyCharacterChunk(d, ids, raw, "batch", fmt.Sprint(i))
		if err != nil {
			t.Fatal("scoped merge", err)
		}
	}
	if len(d.Candidate.Assertions) != 11 || len(d.Candidate.Entities) != 11 {
		t.Fatal("cross-pass collision or loss")
	}
	for _, e := range d.Candidate.Entities {
		if e.Name != e.ID {
			t.Fatal("previous pass overwritten")
		}
	}
	if _, err = CharacterChunks([]string{"same", "same"}, 5); err == nil {
		t.Fatal("duplicate identity accepted")
	}
	if _, err = CharacterChunks(d.IDs, 6); err == nil {
		t.Fatal("oversized model pass accepted")
	}
	p := CharacterProposal{Entities: []domain.Entity{{ID: d.IDs[0], Kind: "character", Name: "越界"}}}
	raw, _ := json.Marshal(p)
	if _, _, err = ApplyCharacterChunk(d, d.IDs[10:], raw, "batch", "bad"); err == nil {
		t.Fatal("previous pass changed by later pass")
	}
}
