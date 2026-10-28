package retention

// ValidateJournalClosure distinguishes live registry heads from explicitly
// historical evidence. Detached history must be reached by an exact source pin
// (including its contiguous predecessors); it can never be a current head.
func ValidateJournalClosure(versions []ReferenceVersion, current map[string]ReferenceVersion) error {
	if len(versions) > 250000 {
		return ErrInvalidInventory
	}
	groups := map[string]map[int64]ReferenceVersion{}
	for _, v := range versions {
		if !validReferenceVersion(v) || v.Reference.Revision > 250000 {
			return ErrInvalidInventory
		}
		id := v.Reference.ID
		if groups[id] == nil {
			groups[id] = map[int64]ReferenceVersion{}
		}
		if _, exists := groups[id][v.Reference.Revision]; exists {
			return ErrInvalidInventory
		}
		groups[id][v.Reference.Revision] = v
	}
	for id, group := range groups {
		last, exists := group[int64(len(group))]
		if !exists {
			return ErrInvalidInventory
		}
		for _, v := range group {
			if v.ProvenanceOnly != last.ProvenanceOnly || v.ProjectID != last.ProjectID {
				return ErrInvalidInventory
			}
		}
		head, live := current[id]
		if last.ProvenanceOnly {
			if live {
				return ErrInvalidInventory
			}
		} else if !live || stableHash(head) != stableHash(last) {
			return ErrInvalidInventory
		}
	}
	covered := map[string]int64{}
	queue := []ReferenceVersion{}
	include := func(id string, revision int64) error {
		group := groups[id]
		if revision < 1 || revision > int64(len(group)) {
			return ErrInvalidInventory
		}
		for n := covered[id] + 1; n <= revision; n++ {
			v, exists := group[n]
			if !exists {
				return ErrInvalidInventory
			}
			queue = append(queue, v)
		}
		covered[id] = max(covered[id], revision)
		return nil
	}
	for id, head := range current {
		if head.ProvenanceOnly || include(id, head.Reference.Revision) != nil {
			return ErrInvalidInventory
		}
	}
	edges := 0
	for i := 0; i < len(queue); i++ {
		v := queue[i]
		for _, pin := range v.Reference.Sources {
			edges++
			parent, exists := groups[pin.ReferenceID][pin.Revision]
			if edges > 1000000 || !exists || parent.ProjectID != v.ProjectID || parent.Reference.PayloadHash != pin.PayloadHash || parent.RecordedAt.After(v.RecordedAt) || include(pin.ReferenceID, pin.Revision) != nil {
				return ErrInvalidInventory
			}
		}
	}
	for id, group := range groups {
		if covered[id] != int64(len(group)) {
			return ErrInvalidInventory
		}
	}
	return ValidateArchivedPins(versions)
}
