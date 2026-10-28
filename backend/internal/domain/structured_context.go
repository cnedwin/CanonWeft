package domain

import (
	"encoding/json"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"
)

const StructuredContextVersion = "structured-context-v1"

type StructuredScope struct {
	FocusEntityIDs       []string `json:"focus_entity_ids,omitempty"`
	RequiredAssertionIDs []string `json:"required_assertion_ids,omitempty"`
}
type StructuredRequest struct {
	Purpose, Perspective, Query string
	StoryTime                   int64
	Scope                       *StructuredScope
	OptionalBytes               int
}
type StructuredSelection struct {
	Version            string `json:"version"`
	SelectedAssertions int    `json:"selected_assertions"`
	OmittedAssertions  int    `json:"omitted_assertions"`
	SelectedEntities   int    `json:"selected_entities"`
	OmittedEntities    int    `json:"omitted_entities"`
	RequiredAssertions int    `json:"required_assertions"`
	RequiredEntities   int    `json:"required_entities"`
}
type StructuredPack struct {
	Facts     []Assertion
	Hidden    []Assertion
	Entities  []Entity
	Rules     []WorldRule
	Selection StructuredSelection
}

func ValidateStructuredScope(scope *StructuredScope) error {
	if scope == nil {
		return nil
	}
	for _, ids := range [][]string{scope.FocusEntityIDs, scope.RequiredAssertionIDs} {
		if len(ids) > 128 {
			return ErrValidation
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if strings.TrimSpace(id) == "" || len(id) > 128 || !utf8.ValidString(id) || seen[id] {
				return ErrValidation
			}
			seen[id] = true
		}
	}
	return nil
}

type structureDelta struct {
	assertions, entities map[string]bool
	bytes                int
}

func newStructureDelta() structureDelta {
	return structureDelta{assertions: map[string]bool{}, entities: map[string]bool{}}
}

type structureBuilder struct {
	assertions map[string]Assertion
	entities   map[string]Entity
	latest     map[string][]string
	known      map[string]bool
	selected   structureDelta
	purpose    string
}

func propertyKey(subject, predicate string) string { return subject + "\x00" + predicate }
func structureBytes(v any) int                     { raw, _ := json.Marshal(v); return len(raw) + 2 }

// Add an assertion and the full entity/profile prerequisite closure as one
// indivisible group. Optional groups either fit completely or are omitted.
func (b *structureBuilder) closure(assertions, entities []string) (structureDelta, error) {
	return b.closureWithin(assertions, entities, -1)
}

// A negative limit denotes a mandatory group. Optional groups stop as soon as
// they cannot fit, without traversing the remaining dependency graph.
func (b *structureBuilder) closureWithin(assertions, entities []string, limit int) (structureDelta, error) {
	d := newStructureDelta()
	var assertion func(string) error
	var entity func(string) error
	assertion = func(id string) error {
		if id == "" || b.selected.assertions[id] || d.assertions[id] {
			return nil
		}
		a, ok := b.assertions[id]
		if !ok {
			return ErrForbidden
		}
		d.assertions[id] = true
		cost := structureBytes(a)
		if b.purpose == "writer" && b.known[id] {
			cost *= 2
		}
		d.bytes += cost
		if limit >= 0 && d.bytes > limit {
			return ErrContextBudget
		}
		for _, ref := range []string{a.Subject, a.ObjectID, a.HolderID} {
			if err := entity(ref); err != nil {
				return err
			}
		}
		for _, ref := range a.KnownBy {
			if err := entity(ref); err != nil {
				return err
			}
		}
		return nil
	}
	entity = func(id string) error {
		if id == "" || b.purpose == "npc" || b.selected.entities[id] || d.entities[id] {
			return nil
		}
		e, ok := b.entities[id]
		if !ok {
			return ErrValidation
		}
		d.entities[id] = true
		d.bytes += structureBytes(e)
		if limit >= 0 && d.bytes > limit {
			return ErrContextBudget
		}
		if e.Profile != nil {
			for _, ref := range e.Profile.Related {
				if err := entity(ref); err != nil {
					return err
				}
			}
			for _, condition := range e.Profile.Requirements {
				if err := entity(condition.Subject); err != nil {
					return err
				}
				for _, ref := range b.latest[propertyKey(condition.Subject, condition.Predicate)] {
					if err := assertion(ref); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for _, id := range entities {
		if err := entity(id); err != nil {
			return d, err
		}
	}
	for _, id := range assertions {
		if err := assertion(id); err != nil {
			return d, err
		}
	}
	return d, nil
}
func (b *structureBuilder) apply(d structureDelta) {
	for id := range d.assertions {
		b.selected.assertions[id] = true
	}
	for id := range d.entities {
		b.selected.entities[id] = true
	}
	b.selected.bytes += d.bytes
}

func (b *structureBuilder) offer(assertions, entities []string, limit int) error {
	remaining := limit - b.selected.bytes
	if remaining <= 0 {
		return nil
	}
	group, err := b.closureWithin(assertions, entities, remaining)
	if err == ErrContextBudget {
		return nil
	}
	if err != nil {
		return err
	}
	b.apply(group)
	return nil
}

func SelectStructured(snapshot Snapshot, req StructuredRequest) (StructuredPack, error) {
	out := StructuredPack{Facts: []Assertion{}, Hidden: []Assertion{}, Entities: []Entity{}, Rules: []WorldRule{}, Selection: StructuredSelection{Version: StructuredContextVersion}}
	if err := ValidateStructuredScope(req.Scope); err != nil {
		return out, err
	}
	if !slices.Contains([]string{"author", "writer", "critic", "research", "npc"}, req.Purpose) || req.OptionalBytes < 0 {
		return out, ErrValidation
	}
	b := structureBuilder{assertions: map[string]Assertion{}, entities: map[string]Entity{}, latest: map[string][]string{}, known: map[string]bool{}, selected: newStructureDelta(), purpose: req.Purpose}
	for _, e := range snapshot.Entities {
		b.entities[e.ID] = e
	}
	// An unbound novelist is a narrator, not an invented character. KnownFacts
	// with an empty identity supplies only public facts; all other assertions
	// remain hidden author constraints. NPCs still require an actual identity.
	if (req.Purpose == "npc" || (req.Purpose == "writer" && req.Perspective != "")) && b.entities[req.Perspective].ID == "" {
		return out, ErrValidation
	}
	available := snapshot.Assertions
	if req.Purpose == "npc" || req.Purpose == "writer" {
		known := KnownFacts(snapshot, req.Perspective, req.StoryTime)
		for _, a := range known {
			b.known[a.ID] = true
		}
		if req.Purpose == "npc" {
			available = known
		}
	}
	latestTime := map[string]int64{}
	allowedEntity := map[string]bool{req.Perspective: true}
	for _, a := range available {
		b.assertions[a.ID] = a
		for _, id := range []string{a.Subject, a.ObjectID, a.HolderID} {
			if id != "" {
				allowedEntity[id] = true
			}
		}
		if !activeFact(a, req.StoryTime) {
			continue
		}
		key := propertyKey(a.Subject, a.Predicate)
		at, exists := latestTime[key]
		if !exists || a.EffectiveFrom > at {
			latestTime[key] = a.EffectiveFrom
			b.latest[key] = []string{a.ID}
		} else if a.EffectiveFrom == at {
			b.latest[key] = append(b.latest[key], a.ID)
		}
	}
	requiredA, requiredE := []string{}, []string{}
	focus := map[string]bool{}
	if req.Scope != nil {
		requiredA = append(requiredA, req.Scope.RequiredAssertionIDs...)
		for _, id := range req.Scope.FocusEntityIDs {
			if b.entities[id].ID == "" {
				if req.Purpose == "npc" {
					return out, ErrForbidden
				}
				return out, ErrValidation
			}
			if req.Purpose == "npc" && !allowedEntity[id] {
				return out, ErrForbidden
			}
			requiredE = append(requiredE, id)
			focus[id] = true
		}
	}
	if req.Perspective != "" && (req.Purpose == "npc" || req.Purpose == "writer") {
		focus[req.Perspective] = true
		requiredE = append(requiredE, req.Perspective)
	}
	if req.Purpose != "npc" {
		_, matches := ExpandQuery(req.Query, snapshot.Entities)
		for _, id := range matches {
			focus[id] = true
		}
		for _, e := range snapshot.Entities {
			if e.Locked {
				requiredE = append(requiredE, e.ID)
			}
		}
		out.Rules = append(out.Rules, snapshot.Rules...)
		for _, rule := range snapshot.Rules {
			conditions := append([]PredicateTest{{Subject: rule.Subject, Predicate: rule.Predicate}}, rule.When...)
			for _, exception := range rule.Exceptions {
				conditions = append(conditions, exception.When...)
			}
			for _, condition := range conditions {
				requiredE = append(requiredE, condition.Subject)
				requiredA = append(requiredA, b.latest[propertyKey(condition.Subject, condition.Predicate)]...)
			}
		}
	}
	for _, a := range available {
		if a.Locked {
			requiredA = append(requiredA, a.ID)
		}
	}
	mandatory, err := b.closure(requiredA, requiredE)
	if err != nil {
		return out, err
	}
	b.apply(mandatory)
	out.Selection.RequiredAssertions = len(b.selected.assertions)
	out.Selection.RequiredEntities = len(b.selected.entities)
	// Keep optional selection bounded independently of total hard constraints.
	// Hard constraints may exceed this share; the outer compiler reports overflow.
	optionalLimit := max(b.selected.bytes, req.OptionalBytes)
	// A named identity can be relevant before it has any assertions. Offer its
	// complete profile before unrelated history consumes the optional budget.
	entityIDs := make([]string, 0, len(b.entities))
	for id := range b.entities {
		entityIDs = append(entityIDs, id)
	}
	sort.Strings(entityIDs)
	if req.Purpose != "npc" {
		for _, id := range entityIDs {
			if !focus[id] {
				continue
			}
			if err := b.offer(nil, []string{id}, optionalLimit); err != nil {
				return out, err
			}
		}
	}
	type ranked struct {
		id      string
		score   int
		current bool
		time    int64
	}
	ranks := make([]ranked, 0, len(available))
	query := strings.ToLower(strings.TrimSpace(req.Query))
	for _, a := range available {
		score := 0
		if focus[a.Subject] {
			score += 100
		}
		if focus[a.ObjectID] || focus[a.HolderID] {
			score += 80
		}
		if query != "" && strings.Contains(strings.ToLower(a.Predicate+" "+a.Value), query) {
			score += 40
		}
		ranks = append(ranks, ranked{id: a.ID, score: score, current: activeFact(a, req.StoryTime), time: a.EffectiveFrom})
	}
	sort.Slice(ranks, func(i, j int) bool {
		a, c := ranks[i], ranks[j]
		if a.score != c.score {
			return a.score > c.score
		}
		if a.current != c.current {
			return a.current
		}
		if a.time != c.time {
			return a.time > c.time
		}
		return a.id < c.id
	})
	for _, candidate := range ranks {
		if b.selected.assertions[candidate.id] {
			continue
		}
		if err := b.offer([]string{candidate.id}, nil, optionalLimit); err != nil {
			return out, err
		}
	}
	// Small projects retain all entity definitions when they fit; large projects
	// retain selected statement identities and their complete prerequisite closure.
	if req.Purpose != "npc" {
		for _, id := range entityIDs {
			if err := b.offer(nil, []string{id}, optionalLimit); err != nil {
				return out, err
			}
		}
	}
	assertionIDs := make([]string, 0, len(b.selected.assertions))
	for id := range b.selected.assertions {
		assertionIDs = append(assertionIDs, id)
	}
	sort.Strings(assertionIDs)
	for _, id := range assertionIDs {
		a := b.assertions[id]
		if req.Purpose == "writer" {
			out.Hidden = append(out.Hidden, a)
			if b.known[id] {
				out.Facts = append(out.Facts, a)
			}
		} else {
			out.Facts = append(out.Facts, a)
		}
	}
	sort.Strings(entityIDs)
	for _, id := range entityIDs {
		if b.selected.entities[id] {
			out.Entities = append(out.Entities, b.entities[id])
		}
	}
	out.Selection.SelectedAssertions = len(assertionIDs)
	out.Selection.OmittedAssertions = len(available) - len(assertionIDs)
	out.Selection.SelectedEntities = len(out.Entities)
	if req.Purpose != "npc" {
		out.Selection.OmittedEntities = len(snapshot.Entities) - len(out.Entities)
	}
	return out, nil
}
