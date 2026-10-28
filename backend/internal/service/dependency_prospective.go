package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
)

// Resolve explicit prospective references before hashing the immutable manifest.
// Forward references are supported; recursive same-version content hashes are not.
// Preparation never changes an existing/pinned reference implicitly.
func (s *IP) resolveProspectiveDependencies(ctx context.Context, a domain.Actor, project string, m *domain.Manifest) error {
	tx, _, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	positions := map[string]int{}
	for i := range m.Artifacts {
		positions[m.Artifacts[i].ResourceID] = i
		if m.Artifacts[i].Dependencies != nil {
			raw, _ := json.Marshal(m.Artifacts[i].Dependencies)
			var copy domain.DependencyPolicy
			if err = json.Unmarshal(raw, &copy); err != nil {
				return err
			}
			m.Artifacts[i].Dependencies = &copy
		}
	}
	state := map[string]int{}
	var resolve func(int) error
	resolve = func(i int) error {
		ref := &m.Artifacts[i]
		if state[ref.ResourceID] == 2 {
			return nil
		}
		if state[ref.ResourceID] == 1 {
			return domain.ErrValidation
		}
		state[ref.ResourceID] = 1
		if ref.Dependencies != nil {
			for j := range ref.Dependencies.Inputs {
				d := &ref.Dependencies.Inputs[j]
				if d.Source.Revision != "$release" && d.Source.Revision != "$candidate" {
					continue
				}
				if d.Source.BranchID != m.BranchID {
					return domain.ErrForbidden
				}
				expected := d.Source.Hash
				switch d.Source.Revision {
				case "$release":
					index, ok := positions[d.Source.ID]
					if d.Source.Kind != "resource" || !ok {
						return domain.ErrValidation
					}
					if err := resolve(index); err != nil {
						return err
					}
					source := m.Artifacts[index]
					d.Source.Revision = m.ReleaseID
					d.Source.Hash = resourceNodeHash(source.Hash, source.Policy, source.Dependencies)
				case "$candidate":
					if m.IPCandidateID == "" {
						return domain.ErrValidation
					}
					c, err := candidateTx(ctx, tx, m.BranchID, m.IPCandidateID)
					if err != nil {
						return err
					}
					if c.Status != "READY" {
						return domain.ErrConflict
					}
					found := false
					for _, entry := range snapshotEntries(m.BranchID, c.ID, "", c.Snapshot) {
						if entry.Ref.Kind == d.Source.Kind && entry.Ref.ID == d.Source.ID {
							d.Source = entry.Ref
							found = true
							break
						}
					}
					if !found {
						return domain.ErrValidation
					}
				}
				if expected != "" && expected != d.Source.Hash {
					return domain.ErrConflict
				}
			}
		}
		state[ref.ResourceID] = 2
		return nil
	}
	for i := range m.Artifacts {
		if err = resolve(i); err != nil {
			return err
		}
	}
	return nil
}

func prospectiveDependency(ctx context.Context, tx *sql.Tx, m domain.Manifest, d domain.Dependency) (bool, error) {
	if m.ReleaseID != "" && d.Source.Revision == m.ReleaseID {
		if d.Source.Kind != "resource" || d.Source.BranchID != m.BranchID {
			return true, domain.ErrForbidden
		}
		for _, ref := range m.Artifacts {
			if ref.ResourceID == d.Source.ID && d.Source.Hash == resourceNodeHash(ref.Hash, ref.Policy, ref.Dependencies) {
				return true, nil
			}
		}
		return true, domain.ErrConflict
	}
	if m.IPCandidateID != "" && d.Source.Revision == m.IPCandidateID {
		if d.Source.BranchID != m.BranchID {
			return true, domain.ErrForbidden
		}
		c, err := candidateTx(ctx, tx, m.BranchID, m.IPCandidateID)
		if err != nil {
			return true, err
		}
		if c.Status != "READY" {
			return true, domain.ErrConflict
		}
		for _, entry := range snapshotEntries(m.BranchID, c.ID, "", c.Snapshot) {
			if entry.Ref == d.Source {
				return true, nil
			}
		}
		return true, domain.ErrConflict
	}
	return false, nil
}
