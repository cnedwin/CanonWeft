package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"slices"
	"strings"
)

func (s *IP) appendResourceImpact(ctx context.Context, tx *sql.Tx, project string, plan *ChangePlan, release domain.Release) error {
	return s.appendResourceImpactFrom(ctx, tx, project, plan, release, false)
}

func (s *IP) appendResourceImpactFrom(ctx context.Context, tx *sql.Tx, project string, plan *ChangePlan, release domain.Release, historical bool) error {
	for _, ref := range release.Manifest.Artifacts {
		old, err := resourceTx(ctx, tx, project, ref.ResourceID)
		if err != nil {
			return err
		}
		if historical {
			if old.BranchID != release.Manifest.BranchID {
				return domain.ErrNotReady
			}
			old.Head, old.Hash, old.Policy, old.Dependencies = ref.ExpectedHead, "", nil, nil
			if ref.ExpectedHead != "" {
				prior, err := releaseTx(ctx, tx, project, ref.ExpectedHead)
				if err != nil {
					return err
				}
				if prior.Status != "PUBLISHED" || prior.Manifest.BranchID != old.BranchID {
					return domain.ErrNotReady
				}
				found := false
				for _, previous := range prior.Manifest.Artifacts {
					if previous.ResourceID == old.ID {
						old.Hash, old.Policy, old.Dependencies = previous.Hash, previous.Policy, previous.Dependencies
						found = true
						break
					}
				}
				if !found {
					return domain.ErrNotReady
				}
			}
		}
		before := domain.DependencyRef{Kind: "resource", BranchID: old.BranchID, ID: old.ID, Revision: old.Head, Hash: resourceNodeHash(old.Hash, old.Policy, old.Dependencies)}
		after := domain.DependencyRef{Kind: "resource", BranchID: old.BranchID, ID: old.ID, Revision: release.ID, Hash: resourceNodeHash(ref.Hash, ref.Policy, ref.Dependencies)}
		if before.Hash == after.Hash {
			continue
		}
		if old.Head == "" {
			before = domain.DependencyRef{}
		}
		plan.Changes = append(plan.Changes, domain.DependencyChange{Before: before, After: after})
		diff := DependencyDifference{Ref: after, Name: old.Name, Fields: []ChangedField{}}
		if old.Hash != ref.Hash {
			var previous []byte
			if old.Hash != "" {
				previous, err = s.Blobs.ReadObject(ctx, project, old.Hash)
				if err != nil {
					return domain.ErrNotReady
				}
			}
			updated, err := s.Blobs.ReadObject(ctx, project, ref.Hash)
			if err != nil {
				return domain.ErrNotReady
			}
			diff.Fields = append(diff.Fields, ChangedField{Name: "text", Before: string(previous), After: string(updated)})
		}
		oldPolicy, _ := json.Marshal(old.Policy)
		newPolicy, _ := json.Marshal(ref.Policy)
		if string(oldPolicy) != string(newPolicy) {
			diff.Fields = append(diff.Fields, ChangedField{Name: "policy", Before: string(oldPolicy), After: string(newPolicy)})
		}
		oldDeps, _ := json.Marshal(old.Dependencies)
		newDeps, _ := json.Marshal(ref.Dependencies)
		if string(oldDeps) != string(newDeps) {
			diff.Fields = append(diff.Fields, ChangedField{Name: "dependencies", Before: string(oldDeps), After: string(newDeps)})
		}
		plan.Differences = append(plan.Differences, diff)
	}
	slices.SortFunc(plan.Changes, func(a, b domain.DependencyChange) int {
		return strings.Compare(a.Before.Key()+a.After.Key(), b.Before.Key()+b.After.Key())
	})
	return nil
}

type ChangePlanSummary struct {
	Retention     *RuntimeRetention `json:"retention,omitempty"`
	SourceEventID string            `json:"source_event_id,omitempty"`
	SourceState   string            `json:"source_state,omitempty"`
	ID            string            `json:"id"`
	CandidateID   string            `json:"candidate_id"`
	ReleaseID     string            `json:"release_id"`
	Changes       int               `json:"changes"`
	Impacts       int               `json:"impacts"`
	CreatedAt     string            `json:"created_at"`
}

func (s *IP) ChangePlans(ctx context.Context, a domain.Actor, project, branch string) ([]ChangePlanSummary, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,CAST(created_at AS CHAR) FROM change_plans WHERE project_id=? AND branch_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project, branch)
	if err != nil {
		return nil, err
	}
	result := []ChangePlanSummary{}
	for rows.Next() {
		var item ChangePlanSummary
		if err = rows.Scan(&item.ID, &item.CreatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		plan, err := readChangePlanTx(ctx, tx, project, result[i].ID)
		if err == domain.ErrMaterialExpired {
			result[i].Retention, err = runtimeTransientTx(ctx, tx, project, "change_plans", "payload", result[i].ID)
			if err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		result[i].CandidateID = plan.CandidateID
		result[i].SourceEventID = plan.SourceEventID
		result[i].SourceState = plan.SourceState
		result[i].ReleaseID = plan.ReleaseID
		result[i].Changes = len(plan.Changes)
		result[i].Impacts = len(plan.Items)
	}
	return result, nil
}
