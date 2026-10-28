package retention

import "time"

// ValidateArchivedPins checks all historical versions, including superseded
// ones not reachable from the current head, and their inherited expiry dates.
// Online cleanup still requires the current protected project lifecycle.
func ValidateArchivedPins(versions []ReferenceVersion) error {
	_, err := ResolveArchivedDeadlines(versions, nil)
	return err
}

type archivedVersionDeadline struct {
	project                     string
	recordedAt, active, deleted time.Time
}

// ArchivedDeadlines is a bounded index of historical version deadlines. It is
// independent of source body contents and grants no live cleanup authority.
type ArchivedDeadlines struct {
	versions   map[referenceVersionKey]archivedVersionDeadline
	lifecycles map[string]Lifecycle
}

// At resolves the exact pinned version under the project's lifecycle at the
// historical cutoff. Later deletion cannot retroactively rewrite older plans.
func (d *ArchivedDeadlines) At(project, id string, revision int64, at time.Time) (time.Time, error) {
	if d == nil || at.IsZero() {
		return time.Time{}, ErrInvalidInventory
	}
	v, exists := d.versions[referenceVersionKey{id, revision}]
	if !exists || v.project != project || v.recordedAt.After(at) {
		return time.Time{}, ErrInvalidInventory
	}
	if !d.lifecycleAt(project, at).DeletedAt.IsZero() {
		return v.deleted, nil
	}
	return v.active, nil
}

func (d *ArchivedDeadlines) lifecycleAt(project string, at time.Time) Lifecycle {
	lifecycle := d.lifecycles[project]
	if !lifecycle.DeletedAt.IsZero() && !lifecycle.DeletedAt.After(at) {
		return lifecycle
	}
	return Lifecycle{}
}

// ResolveArchivedDeadlines uses two linear graph passes in one traversal:
// active-project deadlines and deadlines capped by the original deletion event.
// supplied must contain every referenced project when non-nil. When nil, only
// lifecycles present in immutable history can be checked; archive admission also
// supplies independently validated project lifecycle rows.
func ResolveArchivedDeadlines(versions []ReferenceVersion, supplied map[string]Lifecycle) (*ArchivedDeadlines, error) {
	if len(versions) > 250000 {
		return nil, ErrInvalidInventory
	}
	result := &ArchivedDeadlines{versions: map[referenceVersionKey]archivedVersionDeadline{}, lifecycles: map[string]Lifecycle{}}
	for project, lifecycle := range supplied {
		if !id32.MatchString(project) || !validLifecycle(lifecycle) {
			return nil, ErrInvalidInventory
		}
		result.lifecycles[project] = lifecycle
	}
	positions := map[referenceVersionKey]int{}
	for i, v := range versions {
		if !validReferenceVersion(v) {
			return nil, ErrInvalidInventory
		}
		lifecycle, exists := result.lifecycles[v.ProjectID]
		if supplied != nil && !exists {
			return nil, ErrInvalidInventory
		}
		if v.Lifecycle != (Lifecycle{}) {
			if (supplied != nil || !lifecycle.DeletedAt.IsZero()) && lifecycle != v.Lifecycle {
				return nil, ErrInvalidInventory
			}
			result.lifecycles[v.ProjectID] = v.Lifecycle
		} else if !exists {
			result.lifecycles[v.ProjectID] = Lifecycle{}
		}
		key := referenceKey(v.Reference)
		if _, duplicate := positions[key]; duplicate {
			return nil, ErrInvalidInventory
		}
		positions[key] = i
	}
	for _, v := range versions {
		born, err := referenceBirth(v.Reference, v.RecordedAt)
		if err != nil {
			return nil, err
		}
		class := v.Class
		if v.Reference.ContentRoot {
			class = Content
		}
		active, err := Deadline(class, born, Lifecycle{})
		if err != nil {
			return nil, err
		}
		deleted, err := Deadline(class, born, result.lifecycles[v.ProjectID])
		if err != nil {
			return nil, err
		}
		result.versions[referenceKey(v.Reference)] = archivedVersionDeadline{project: v.ProjectID, recordedAt: v.RecordedAt, active: active, deleted: deleted}
	}
	remaining := make([]int, len(versions))
	children := make([][]int, len(versions))
	ready := make([]int, 0, len(versions))
	edges := 0
	for i, v := range versions {
		edges += len(v.Reference.Sources)
		if edges > 1000000 {
			return nil, ErrInvalidInventory
		}
		remaining[i] = len(v.Reference.Sources)
		if remaining[i] == 0 {
			ready = append(ready, i)
		}
		for _, pin := range v.Reference.Sources {
			position, exists := positions[pinKey(pin)]
			if !exists {
				return nil, ErrInvalidInventory
			}
			parent := versions[position]
			rule, ok := ConcreteRule(parent.Reference.Rule)
			if !ok || parent.ProjectID != v.ProjectID || parent.Reference.PayloadHash != pin.PayloadHash || parent.RecordedAt.After(v.RecordedAt) || rule.Storage == Metadata || rule.Class == Audit || rule.Class == ProtectedAuthority {
				return nil, ErrInvalidInventory
			}
			children[position] = append(children[position], i)
		}
	}
	for cursor := 0; cursor < len(ready); cursor++ {
		parent := result.versions[referenceKey(versions[ready[cursor]].Reference)]
		for _, child := range children[ready[cursor]] {
			key := referenceKey(versions[child].Reference)
			d := result.versions[key]
			if !parent.active.IsZero() && (d.active.IsZero() || parent.active.Before(d.active)) {
				d.active = parent.active
			}
			if !parent.deleted.IsZero() && (d.deleted.IsZero() || parent.deleted.Before(d.deleted)) {
				d.deleted = parent.deleted
			}
			result.versions[key] = d
			remaining[child]--
			if remaining[child] == 0 {
				ready = append(ready, child)
			}
		}
	}
	if len(ready) != len(versions) {
		return nil, ErrInvalidInventory
	}
	for _, v := range versions {
		if v.Schema != 2 {
			continue
		}
		deadline, err := result.At(v.ProjectID, v.Reference.ID, v.Reference.Revision, v.Reference.ExpiredAt)
		if err != nil || deadline.IsZero() || !deadline.Equal(v.Deadline) || v.Reference.ExpiredAt.Before(deadline) || v.Lifecycle != result.lifecycleAt(v.ProjectID, v.Reference.ExpiredAt) {
			return nil, ErrInvalidInventory
		}
	}
	return result, nil
}
