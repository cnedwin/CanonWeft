package retention

import "time"

// referenceDeadlines resolves an explicitly registered lineage in linear time.
// No recursion is used: a corrupt or very deep chain must not exhaust the Go
// stack. Missing historical source versions block the plan; they must never be
// silently rebound to a newly edited source or to the copy's creation time.
func referenceDeadlines(refs []Reference, lifecycle Lifecycle, now time.Time) (map[string]time.Time, error) {
	return referenceDeadlinesWithHistory(refs, nil, lifecycle, now)
}

type referenceVersionKey struct {
	ID       string
	Revision int64
}

func referenceKey(ref Reference) referenceVersionKey {
	return referenceVersionKey{ref.ID, ref.Revision}
}

func pinKey(pin ReferenceSource) referenceVersionKey {
	return referenceVersionKey{pin.ReferenceID, pin.Revision}
}

func referenceDeadlinesWithHistory(current, historical []Reference, lifecycle Lifecycle, now time.Time) (map[string]time.Time, error) {
	if len(current)+len(historical) > 250000 {
		return nil, ErrInvalidInventory
	}
	refs := make([]Reference, 0, len(current)+len(historical))
	refs = append(refs, current...)
	refs = append(refs, historical...)
	positions := make(map[referenceVersionKey]int, len(refs))
	deadlines := make([]time.Time, len(refs))
	currentIDs := make(map[string]bool, len(current))
	for _, ref := range current {
		if currentIDs[ref.ID] {
			return nil, ErrInvalidInventory
		}
		currentIDs[ref.ID] = true
	}
	rules := make([]ReferenceRule, len(refs))
	edges := 0
	for i, ref := range refs {
		rule, ok := ConcreteRule(ref.Rule)
		if !ok || !hash64.MatchString(ref.ID) || !hash64.MatchString(ref.PayloadHash) || ref.Revision < 1 || (ref.ContentRoot && !rule.Promotable) || len(ref.Sources) > 4096 {
			return nil, ErrInvalidInventory
		}
		if _, duplicate := positions[referenceKey(ref)]; duplicate {
			return nil, ErrInvalidInventory
		}
		if (rule.Storage == Blob && !hash64.MatchString(ref.ObjectHash)) || (rule.Storage != Blob && ref.ObjectHash != "") || ref.LeaseUntil.After(now.Add(day)) || (ref.AccountingOpen && (rule.Class != Audit || ref.State == Expired)) {
			return nil, ErrInvalidInventory
		}
		// Accepted author content is an independent preservation root. Raw
		// copies cannot acquire that status through a lineage declaration.
		// Accounting projections have their own legally meaningful lifetime;
		// they must not expire merely because the raw evidence was removed.
		if len(ref.Sources) > 0 && (ref.ContentRoot || rule.Storage == Metadata || rule.Class == Audit || rule.Class == ProtectedAuthority) {
			return nil, ErrInvalidInventory
		}
		edges += len(ref.Sources)
		if edges > 1000000 {
			return nil, ErrInvalidInventory
		}
		born, err := referenceBirth(ref, now)
		if err != nil {
			return nil, err
		}
		class := rule.Class
		if ref.ContentRoot {
			class = Content
		}
		deadline, err := Deadline(class, born, lifecycle)
		if err != nil {
			return nil, err
		}
		if (ref.State != Retained && ref.State != Expired) || (ref.State == Retained && !ref.ExpiredAt.IsZero()) || (ref.State == Expired && (ref.ExpiredAt.IsZero() || ref.ExpiredAt.After(now))) {
			return nil, ErrInvalidInventory
		}
		positions[referenceKey(ref)], deadlines[i], rules[i] = i, deadline, rule
	}

	remaining := make([]int, len(refs))
	children := make([][]int, len(refs))
	ready := make([]int, 0, len(refs))
	for i, ref := range refs {
		seen := make(map[string]bool, len(ref.Sources))
		for _, pin := range ref.Sources {
			parent, exists := positions[pinKey(pin)]
			if !exists || ref.ID == pin.ReferenceID || seen[pin.ReferenceID] || pin.PayloadHash != refs[parent].PayloadHash {
				return nil, ErrInvalidInventory
			}
			if rules[parent].Storage == Metadata || rules[parent].Class == Audit || rules[parent].Class == ProtectedAuthority {
				return nil, ErrInvalidInventory
			}
			seen[pin.ReferenceID] = true
			children[parent] = append(children[parent], i)
		}
		remaining[i] = len(ref.Sources)
		if remaining[i] == 0 {
			ready = append(ready, i)
		}
	}
	// Historical evidence must be reachable from a current reference. It is
	// never an extra live reference or a way to smuggle unrelated proof.
	reachable := make([]bool, len(refs))
	queue := make([]int, len(current))
	for i := range current {
		reachable[i] = true
		queue[i] = i
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		for _, pin := range refs[queue[cursor]].Sources {
			parent := positions[pinKey(pin)]
			if !reachable[parent] {
				reachable[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	if len(queue) != len(refs) {
		return nil, ErrInvalidInventory
	}
	for cursor := 0; cursor < len(ready); cursor++ {
		parent := ready[cursor]
		inherited := deadlines[parent]
		for _, child := range children[parent] {
			own := deadlines[child]
			if !inherited.IsZero() && (own.IsZero() || inherited.Before(own)) {
				deadlines[child] = inherited
			}
			remaining[child]--
			if remaining[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if len(ready) != len(refs) {
		return nil, ErrInvalidInventory
	}
	for i, ref := range refs {
		if ref.State == Expired && (deadlines[i].IsZero() || ref.ExpiredAt.Before(deadlines[i])) {
			return nil, ErrInvalidInventory
		}
	}
	result := make(map[string]time.Time, len(current))
	for i, ref := range current {
		result[ref.ID] = deadlines[i]
	}
	return result, nil
}
