package retention

import (
	"math"
	"slices"
	"strings"
	"time"
)

// Prepare the complete revision assignment before writing any row. New direct
// dependencies use their parent's assigned revision; unchanged copies retain
// their original pins, even when that parent is edited or adopted later.
func prepareMaterialVersions(project string, lifecycle Lifecycle, materials []ObservedMaterial, current map[string]ReferenceVersion, now time.Time) ([]ReferenceVersion, int, error) {
	next := make(map[string]ReferenceVersion, len(materials))
	children := map[string][]string{}
	remaining := map[string]int{}
	fixed := map[string][]ReferenceSource{}
	links := map[string][]MaterialSource{}
	unbound := 0
	for _, m := range materials {
		unbound += m.UnboundSources
		if _, duplicate := next[m.ID]; duplicate {
			return nil, 0, ErrInvalidInventory
		}
		v, err := materialReferenceVersion(project, m, lifecycle, now)
		if err != nil {
			return nil, 0, err
		}
		next[m.ID] = v
	}
	for _, m := range materials {
		v := next[m.ID]
		prior, exists := current[m.ID]
		if exists && (!prior.Reference.FirstVerifiedAt.Equal(v.Reference.FirstVerifiedAt) || prior.Reference.TimeBasis != v.Reference.TimeBasis || prior.SourceID != v.SourceID) {
			return nil, 0, ErrInvalidInventory
		}
		preserve := exists && !v.Reference.ContentRoot && prior.Reference.PayloadHash == v.Reference.PayloadHash && prior.SourcePayloadHash == v.SourcePayloadHash && len(prior.Reference.Sources) > 0
		preserved := map[string]bool{}
		if preserve {
			targets := map[string]bool{}
			for _, source := range m.Sources {
				targets[source.ReferenceID] = true
			}
			for _, pin := range prior.Reference.Sources {
				if !targets[pin.ReferenceID] {
					return nil, 0, ErrInvalidInventory
				}
				preserved[pin.ReferenceID] = true
			}
			fixed[m.ID] = slices.Clone(prior.Reference.Sources)
		}
		seen := map[string]bool{}
		for _, source := range m.Sources {
			if !hash64.MatchString(source.ReferenceID) || source.ReferenceID == m.ID || seen[source.ReferenceID] || source.Revision < 0 {
				return nil, 0, ErrInvalidInventory
			}
			seen[source.ReferenceID] = true
			if preserved[source.ReferenceID] {
				continue
			}
			if source.Unresolved {
				unbound++
				continue
			}
			if (source.DraftVersion != nil || source.WorkingProjection != nil) && source.PayloadHash == "" {
				unbound++
				continue
			}
			if !hash64.MatchString(source.PayloadHash) {
				return nil, 0, ErrInvalidInventory
			}
			if source.Revision > 0 {
				fixed[m.ID] = append(fixed[m.ID], ReferenceSource{source.ReferenceID, source.Revision, source.PayloadHash})
				continue
			}
			if parent, ok := next[source.ReferenceID]; ok && parent.Reference.PayloadHash == source.PayloadHash && sourceRequestMatches(source, parent.DraftVersion, parent.WorkingProjection, parent.Reference.PayloadHash) {
				links[m.ID] = append(links[m.ID], source)
				children[source.ReferenceID] = append(children[source.ReferenceID], m.ID)
				remaining[m.ID]++
			} else if parent, ok := current[source.ReferenceID]; ok && parent.Reference.PayloadHash == source.PayloadHash && sourceRequestMatches(source, parent.DraftVersion, parent.WorkingProjection, parent.Reference.PayloadHash) {
				fixed[m.ID] = append(fixed[m.ID], ReferenceSource{parent.Reference.ID, parent.Reference.Revision, parent.Reference.PayloadHash})
			} else {
				// No original version was observed (e.g. a late receipt whose call
				// output had already been discarded). Never invent a replacement.
				unbound++
			}
		}
	}
	queue := make([]string, 0, len(materials))
	for _, m := range materials {
		if remaining[m.ID] == 0 {
			queue = append(queue, m.ID)
		}
	}
	prepared := make(map[string]ReferenceVersion, len(materials))
	for cursor := 0; cursor < len(queue); cursor++ {
		id := queue[cursor]
		v := next[id]
		v.Reference.Sources = slices.Clone(fixed[id])
		for _, link := range links[id] {
			parent, ok := prepared[link.ReferenceID]
			if !ok || parent.Reference.PayloadHash != link.PayloadHash {
				return nil, 0, ErrInvalidInventory
			}
			v.Reference.Sources = append(v.Reference.Sources, ReferenceSource{parent.Reference.ID, parent.Reference.Revision, parent.Reference.PayloadHash})
		}
		slices.SortFunc(v.Reference.Sources, func(a, b ReferenceSource) int { return strings.Compare(a.ReferenceID, b.ReferenceID) })
		if len(v.Reference.Sources) > 0 {
			v.LineageState = "PARTIAL"
		}
		if prior, exists := current[id]; exists {
			v.Reference.Revision = prior.Reference.Revision
			v.Reference.LeaseUntil = prior.Reference.LeaseUntil
			v.RecordedAt = prior.RecordedAt
			if stableHash(v) != stableHash(prior) {
				if v.Reference.Revision == math.MaxInt64 {
					return nil, 0, ErrInvalidInventory
				}
				v.Reference.Revision++
				v.RecordedAt = now.UTC()
			}
		}
		if !validReferenceVersion(v) {
			return nil, 0, ErrInvalidInventory
		}
		prepared[id] = v
		for _, child := range children[id] {
			remaining[child]--
			if remaining[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if len(prepared) != len(materials) {
		return nil, 0, ErrInvalidInventory
	}
	result := make([]ReferenceVersion, 0, len(materials))
	for _, m := range materials {
		result = append(result, prepared[m.ID])
	}
	return result, unbound, nil
}
