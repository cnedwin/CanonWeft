package novel

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

// Design IDs identify candidates before a model is called. Names never identify
// or merge people, and an interrupted apply can be retried without new IDs.
func CharacterDesignID(batch, kind, key string) string {
	h := sha256.Sum256([]byte("character-design-v1\x00" + batch + "\x00" + kind + "\x00" + key))
	return hex.EncodeToString(h[:16])
}

type CharacterProposal struct {
	Entities    []domain.Entity    `json:"entities"`
	Assertions  []domain.Assertion `json:"assertions"`
	Explanation string             `json:"explanation"`
	Warnings    []string           `json:"warnings"`
}

// Model output contains only the requested cards. The server preserves all
// other world data and assigns stable IDs to new assertions.
func ApplyCharacterProposal(d CharacterDesign, raw []byte, batch, generation string) (CharacterDesign, CharacterProposal, error) {
	var p CharacterProposal
	if len(raw) > 256*1024 || json.Unmarshal(raw, &p) != nil || len(p.Entities) != len(d.IDs) {
		return d, p, domain.ErrValidation
	}
	next := d
	next.Candidate.Entities = slices.Clone(d.Candidate.Entities)
	next.Candidate.Assertions = slices.Clone(d.Candidate.Assertions)
	next.Candidate.Entities = slices.DeleteFunc(next.Candidate.Entities, func(e domain.Entity) bool { return slices.Contains(d.IDs, e.ID) })
	next.Candidate.Assertions = slices.DeleteFunc(next.Candidate.Assertions, func(a domain.Assertion) bool { return slices.Contains(d.IDs, a.Subject) })
	for _, e := range p.Entities {
		if !slices.Contains(d.IDs, e.ID) {
			return d, p, domain.ErrValidation
		}
		next.Candidate.Entities = append(next.Candidate.Entities, e)
	}
	existing := map[string]bool{}
	for _, a := range d.Candidate.Assertions {
		existing[a.ID] = true
	}
	for _, a := range p.Assertions {
		if !slices.Contains(d.IDs, a.Subject) || strings.TrimSpace(a.ID) == "" {
			return d, p, domain.ErrValidation
		}
		if !existing[a.ID] {
			a.ID = CharacterDesignID(batch, "assertion", a.Subject+"\x00"+a.ID)
			a.Source = "character-design:" + batch + ":generation:" + generation
		}
		next.Candidate.Assertions = append(next.Candidate.Assertions, a)
	}
	next.GenerationID = generation
	next.RecipeRunID = ""
	if err := ValidateCharacterDesign(next); err != nil {
		return d, p, err
	}
	if err := domain.CheckLocked(d.Candidate, next.Candidate); err != nil {
		return d, p, err
	}
	return next, p, nil
}

type CharacterDesign struct {
	Schema       string          `json:"schema"`
	BookID       string          `json:"book_id"`
	Notes        string          `json:"notes"`
	Genre        string          `json:"genre"`
	IDs          []string        `json:"ids"`
	Base         domain.Snapshot `json:"base"`
	Candidate    domain.Snapshot `json:"candidate"`
	GenerationID string          `json:"generation_id,omitempty"`
	RecipeRunID  string          `json:"recipe_run_id,omitempty"`
	SourceIDs    []string        `json:"source_ids,omitempty"`
	Naming       *NamingBoard    `json:"naming,omitempty"`
}

// Each pass edits its assigned identities while preserving all other passes.
func ApplyCharacterChunk(d CharacterDesign, ids []string, raw []byte, batch, generation string) (CharacterDesign, CharacterProposal, error) {
	for _, id := range ids {
		if !slices.Contains(d.IDs, id) {
			return d, CharacterProposal{}, domain.ErrValidation
		}
	}
	scope := d
	scope.IDs = slices.Clone(ids)
	scope.Base = d.Candidate
	next, p, err := ApplyCharacterProposal(scope, raw, batch, generation)
	if err != nil {
		return d, p, err
	}
	d.Candidate = next.Candidate
	if err = ValidateCharacterDesign(d); err != nil {
		return d, p, err
	}
	return d, p, nil
}

func CharacterChunks(ids []string, size int) ([][]string, error) {
	if len(ids) < 1 || len(ids) > 100 || size < 1 || size > 5 || (len(ids)+size-1)/size > 32 {
		return nil, domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" || seen[id] {
			return nil, domain.ErrValidation
		}
		seen[id] = true
	}
	chunks := [][]string{}
	for start := 0; start < len(ids); start += size {
		chunks = append(chunks, slices.Clone(ids[start:min(start+size, len(ids))]))
	}
	return chunks, nil
}

// Only the requested characters may change. Assertions about them can refer to
// existing identities, but a proposal cannot edit unrelated people or world rules.
func ValidateCharacterDesign(d CharacterDesign) error {
	if err := ValidateNamingBoard(d.Naming); err != nil {
		return err
	}
	if len(d.SourceIDs) > 12 {
		return domain.ErrValidation
	}
	sources := map[string]bool{}
	for _, id := range d.SourceIDs {
		if !planID.MatchString(id) || sources[id] {
			return domain.ErrValidation
		}
		sources[id] = true
	}
	if d.Schema != "character-design-v1" || !planID.MatchString(d.BookID) || len(d.IDs) < 1 || len(d.IDs) > 100 || len([]rune(d.Notes)) > 10000 || len([]rune(d.Genre)) > 300 {
		return domain.ErrValidation
	}
	ids := map[string]bool{}
	for _, id := range d.IDs {
		if strings.TrimSpace(id) == "" || len(id) > 128 || ids[id] {
			return domain.ErrValidation
		}
		ids[id] = true
	}
	if err := domain.ValidateSnapshot(d.Base); err != nil {
		return err
	}
	if err := domain.ValidateSnapshot(d.Candidate); err != nil {
		return err
	}
	if err := domain.CheckLocked(d.Base, d.Candidate); err != nil {
		return err
	}
	if !reflect.DeepEqual(d.Base.Rules, d.Candidate.Rules) || !reflect.DeepEqual(d.Base.Events, d.Candidate.Events) || !reflect.DeepEqual(d.Base.Mappings, d.Candidate.Mappings) {
		return domain.ErrValidation
	}
	baseEntities := map[string]domain.Entity{}
	for _, e := range d.Base.Entities {
		baseEntities[e.ID] = e
	}
	found := map[string]bool{}
	for _, e := range d.Candidate.Entities {
		if ids[e.ID] {
			if e.Kind != "character" {
				return domain.ErrValidation
			}
			found[e.ID] = true
			continue
		}
		if original, ok := baseEntities[e.ID]; !ok || !reflect.DeepEqual(original, e) {
			return domain.ErrValidation
		}
		delete(baseEntities, e.ID)
	}
	for id := range baseEntities {
		if !ids[id] {
			return domain.ErrValidation
		}
	}
	if len(found) != len(ids) {
		return fmt.Errorf("%w：候选人物数量与指定身份不符", domain.ErrValidation)
	}
	baseAssertions := map[string]domain.Assertion{}
	for _, a := range d.Base.Assertions {
		baseAssertions[a.ID] = a
	}
	for _, a := range d.Candidate.Assertions {
		old, exists := baseAssertions[a.ID]
		if !ids[a.Subject] && (!exists || !reflect.DeepEqual(old, a)) {
			return domain.ErrValidation
		}
		if exists && old.Subject != a.Subject {
			return domain.ErrValidation
		}
		delete(baseAssertions, a.ID)
	}
	for _, a := range baseAssertions {
		if !ids[a.Subject] {
			return domain.ErrValidation
		}
	}
	return nil
}

// Merge selected complete cards into a fresh private snapshot. Publishing and
// downstream chapter changes remain in the existing author-confirmed IP workflow.
func MergeCharacterDesign(d CharacterDesign, current domain.Snapshot, selected []string) (domain.Snapshot, []string, error) {
	if err := ValidateCharacterDesign(d); err != nil {
		return current, nil, err
	}
	chosen := map[string]bool{}
	for _, id := range selected {
		if chosen[id] || !slices.Contains(d.IDs, id) {
			return current, nil, domain.ErrValidation
		}
		chosen[id] = true
	}
	if len(chosen) == 0 {
		return current, nil, domain.ErrValidation
	}
	if !reflect.DeepEqual(d.Base.Rules, current.Rules) || !reflect.DeepEqual(d.Base.Events, current.Events) || !reflect.DeepEqual(d.Base.Mappings, current.Mappings) {
		return current, nil, domain.ErrConflict
	}
	baseEntities := map[string]domain.Entity{}
	for _, e := range d.Base.Entities {
		baseEntities[e.ID] = e
	}
	candidates := map[string]domain.Entity{}
	for _, e := range d.Candidate.Entities {
		candidates[e.ID] = e
	}
	existing := map[string]domain.Entity{}
	for _, e := range current.Entities {
		existing[e.ID] = e
	}
	// A previously existing identity must still exist. Never resurrect a deleted
	// character merely because an old model result mentioned them.
	for id, base := range baseEntities {
		actual, ok := existing[id]
		if !ok || (!reflect.DeepEqual(base, actual) && !reflect.DeepEqual(candidates[id], actual)) {
			return current, nil, domain.ErrConflict
		}
	}
	out := current
	out.Entities = slices.Clone(current.Entities)
	out.Assertions = slices.Clone(current.Assertions)
	warnings := []string{}
	for _, id := range selected {
		candidate := candidates[id]
		if old, ok := existing[id]; ok {
			if old.Name != candidate.Name && !slices.Contains(candidate.Aliases, old.Name) {
				candidate.Aliases = append(slices.Clone(candidate.Aliases), old.Name)
			}
			if _, wasPresent := baseEntities[id]; !wasPresent && !reflect.DeepEqual(old, candidate) {
				return current, nil, domain.ErrConflict
			}
			for i := range out.Entities {
				if out.Entities[i].ID == id {
					out.Entities[i] = candidate
				}
			}
		} else {
			out.Entities = append(out.Entities, candidate)
		}
		for _, other := range out.Entities {
			if other.ID != id && (strings.EqualFold(strings.TrimSpace(other.Name), strings.TrimSpace(candidate.Name)) || slices.Contains(other.Aliases, candidate.Name) || slices.Contains(candidate.Aliases, other.Name)) {
				warnings = append(warnings, "姓名或别名相近："+candidate.Name+" / "+other.Name+"；保留独立身份，请核对。")
			}
		}
	}
	available := map[string]bool{}
	for _, e := range out.Entities {
		available[e.ID] = true
	}
	baseFacts := map[string]domain.Assertion{}
	for _, a := range d.Base.Assertions {
		baseFacts[a.ID] = a
	}
	newFacts := map[string]domain.Assertion{}
	for _, a := range d.Candidate.Assertions {
		if chosen[a.Subject] {
			newFacts[a.ID] = a
		}
	}
	// Check changed/deleted assertions before replacing the selected card. Even
	// unlocked hand edits made after generation cannot be silently overwritten.
	for _, a := range current.Assertions {
		if !chosen[a.Subject] {
			continue
		}
		old, wasPresent := baseFacts[a.ID]
		next, willExist := newFacts[a.ID]
		if (!wasPresent || !reflect.DeepEqual(a, old)) && (!willExist || !reflect.DeepEqual(a, next)) {
			return current, nil, domain.ErrConflict
		}
	}
	for id, old := range baseFacts {
		if !chosen[old.Subject] {
			continue
		}
		present := false
		for _, a := range current.Assertions {
			if a.ID == id {
				present = true
			}
		}
		if !present {
			return current, nil, domain.ErrConflict
		}
	}
	out.Assertions = slices.DeleteFunc(out.Assertions, func(a domain.Assertion) bool { return chosen[a.Subject] })
	for _, a := range d.Candidate.Assertions {
		if !chosen[a.Subject] {
			continue
		}
		refs := append(slices.Clone(a.KnownBy), a.ObjectID, a.HolderID)
		for _, ref := range refs {
			if ref != "" && !available[ref] {
				return current, nil, fmt.Errorf("%w：请同时选择关系涉及的人物，或修改关系后再加入", domain.ErrValidation)
			}
		}
		out.Assertions = append(out.Assertions, a)
	}
	if err := domain.ValidateSnapshot(out); err != nil {
		return current, nil, err
	}
	if err := domain.CheckLocked(current, out); err != nil {
		return current, nil, err
	}
	return out, warnings, nil
}
