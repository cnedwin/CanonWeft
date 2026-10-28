package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

type PropagationResult struct {
	State  string `json:"state"`
	PlanID string `json:"plan_id,omitempty"`
}

// PropagatePublication is internal maintenance, never a user-authored write.
// The original author is historical attribution only. No model, adoption,
// resource head, draft, credential or spending state can be changed here.
func (s *IP) PropagatePublication(ctx context.Context, eventID string) (PropagationResult, error) {
	var result PropagationResult
	decoded, decodeErr := hex.DecodeString(eventID)
	if decodeErr != nil || len(decoded) != 16 || strings.ToLower(eventID) != eventID {
		return result, domain.ErrValidation
	}
	var project, source, kind string
	err := s.Workspace.DB.QueryRowContext(ctx, "SELECT project_id,resource_id,kind FROM outbox WHERE id=?", eventID).Scan(&project, &source, &kind)
	if err == sql.ErrNoRows {
		// Restored copies intentionally omit the execution outbox.
		return PropagationResult{State: "CANCELLED"}, nil
	}
	if err != nil {
		return result, err
	}
	if kind != "ip.published" && kind != "release.published" {
		return result, domain.ErrValidation
	}
	var sourceActor string
	if kind == "ip.published" {
		err = s.Workspace.DB.QueryRowContext(ctx, "SELECT r.created_by FROM ip_revisions r JOIN branches b ON b.id=r.branch_id WHERE r.id=? AND b.project_id=?", source, project).Scan(&sourceActor)
	} else {
		err = s.Workspace.DB.QueryRowContext(ctx, "SELECT created_by FROM artifact_releases WHERE id=? AND project_id=?", source, project).Scan(&sourceActor)
	}
	if err != nil {
		return result, err
	}
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	// The receipt's created_by FK needs a shared author lock. Acquire it before
	// the project, like normal requests, even if that historical author is disabled.
	// This lock is attribution/integrity only and never grants their permissions.
	var lockedActor string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR SHARE", sourceActor).Scan(&lockedActor); err != nil {
		return result, err
	}
	var deleted sql.NullTime
	err = tx.QueryRowContext(ctx, "SELECT deleted_at FROM projects WHERE id=? FOR UPDATE", project).Scan(&deleted)
	if err == sql.ErrNoRows || (err == nil && deleted.Valid) {
		return PropagationResult{State: "CANCELLED"}, nil
	}
	if err != nil {
		return result, err
	}
	active, err := protectedLedger().ProjectActiveTx(ctx, tx, project)
	if err != nil {
		return result, err
	}
	if !active {
		return PropagationResult{State: "CANCELLED"}, nil
	}
	// Lock in the same order as publication: project before outbox/source rows.
	var lockedProject, lockedSource, lockedKind string
	var eventPayload []byte
	if err = tx.QueryRowContext(ctx, "SELECT project_id,resource_id,kind,payload FROM outbox WHERE id=? FOR UPDATE", eventID).Scan(&lockedProject, &lockedSource, &lockedKind, &eventPayload); err != nil {
		return result, err
	}
	if lockedProject != project || lockedSource != source || lockedKind != kind {
		return result, domain.ErrConflict
	}
	if err := rejectExpiredRuntime(eventPayload, "outbox.payload"); err != nil {
		if err != domain.ErrMaterialExpired {
			return result, err
		}
		if _, err := runtimeTransientTx(ctx, tx, project, "outbox", "payload", eventID); err != nil {
			return result, err
		}
		return PropagationResult{State: "CANCELLED"}, nil
	}
	id := digest("change-propagation-v1:" + eventID)[:32]
	var exists int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM change_plans WHERE id=?", id).Scan(&exists); err != nil {
		return result, err
	}
	if exists != 0 {
		plan, err := readChangePlanTx(ctx, tx, project, id)
		if err != nil {
			return result, err
		}
		if plan.SourceEventID != eventID || plan.SourceEventKind != kind || (kind == "ip.published" && plan.CandidateID != source) || (kind == "release.published" && plan.ReleaseID != source) {
			return result, domain.ErrNotReady
		}
		return PropagationResult{State: "COMPLETED", PlanID: id}, nil
	}
	plan, creator, err := s.publicationPlanTx(ctx, tx, project, source, kind)
	if err != nil {
		return result, err
	}
	if creator != sourceActor {
		return result, domain.ErrConflict
	}
	plan.ID, plan.SourceEventID, plan.SourceEventKind = id, eventID, kind
	raw, err := json.Marshal(plan)
	if err != nil {
		return result, err
	}
	var candidate any
	if plan.CandidateID != "" {
		candidate = plan.CandidateID
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO change_plans(id,project_id,branch_id,candidate_id,payload,content_hash,created_by) VALUES(?,?,?,?,?,?,?)", id, project, plan.BranchID, candidate, raw, valueHash(plan), creator); err != nil {
		return result, err
	}
	if plan.ReleaseID != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO change_plan_releases(plan_id,release_id) VALUES(?,?)", id, plan.ReleaseID); err != nil {
			return result, err
		}
	}
	if err = s.captureChangePlanSourceTx(ctx, tx, project, id); err != nil {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	return PropagationResult{State: "COMPLETED", PlanID: id}, nil
}

func (s *IP) publicationPlanTx(ctx context.Context, tx *sql.Tx, project, source, kind string) (ChangePlan, string, error) {
	plan := ChangePlan{SemanticReview: "NOT_RUN", SourceState: "CURRENT", Changes: []domain.DependencyChange{}, OutsideRecordedClosure: []domain.DependencyRef{}}
	var branchID, creator, candidateID, beforeID string
	var release domain.Release
	var err error
	if kind == "release.published" {
		release, err = releaseTx(ctx, tx, project, source)
		if err != nil {
			return plan, creator, err
		}
		if release.Status != "PUBLISHED" {
			return plan, creator, domain.ErrNotReady
		}
		branchID, candidateID, beforeID = release.Manifest.BranchID, release.Manifest.IPCandidateID, release.Manifest.ExpectedIPHead
		plan.ReleaseID = source
		err = tx.QueryRowContext(ctx, "SELECT created_by FROM artifact_releases WHERE id=? AND project_id=?", source, project).Scan(&creator)
	} else {
		candidateID = source
		err = tx.QueryRowContext(ctx, "SELECT r.branch_id,r.created_by FROM ip_revisions r JOIN branches b ON b.id=r.branch_id WHERE r.id=? AND b.project_id=?", source, project).Scan(&branchID, &creator)
	}
	if err != nil {
		return plan, creator, err
	}
	b, err := branchTx(ctx, tx, project, branchID)
	if err != nil {
		return plan, creator, err
	}
	plan.BranchID, plan.BaseHead, plan.CandidateID = b.ID, b.Head, candidateID
	if candidateID != "" {
		c, err := candidateTx(ctx, tx, branchID, candidateID)
		if err != nil {
			return plan, creator, err
		}
		if c.Status != "PUBLISHED" || valueHash(c.Snapshot) != c.Hash || (kind == "release.published" && c.BaseHead != beforeID) {
			return plan, creator, domain.ErrNotReady
		}
		beforeID = c.BaseHead
		before, err := snapshotTx(ctx, tx, project, beforeID)
		if err != nil {
			return plan, creator, err
		}
		if beforeID != "" {
			var hash string
			if err = tx.QueryRowContext(ctx, "SELECT content_hash FROM ip_revisions WHERE id=?", beforeID).Scan(&hash); err != nil {
				return plan, creator, err
			}
			if hash != valueHash(before) {
				return plan, creator, domain.ErrNotReady
			}
		}
		plan.Changes = publicationSnapshotChanges(snapshotEntries(b.ID, beforeID, b.Name, before), snapshotEntries(b.ID, c.ID, b.Name, c.Snapshot))
		plan.Differences = dependencyDifferences(plan.Changes, before, c.Snapshot)
		if b.Head != c.ID {
			plan.SourceState = "SUPERSEDED"
		}
	} else if b.Head != beforeID {
		plan.SourceState = "SUPERSEDED"
	}
	if plan.ReleaseID != "" {
		if err = s.appendResourceImpactFrom(ctx, tx, project, &plan, release, true); err != nil {
			return plan, creator, err
		}
		for _, ref := range release.Manifest.Artifacts {
			r, err := resourceTx(ctx, tx, project, ref.ResourceID)
			if err != nil {
				return plan, creator, err
			}
			if r.Head != release.ID {
				plan.SourceState = "SUPERSEDED"
			}
		}
	}
	g, err := dependencyGraphTx(ctx, tx, project)
	if err != nil {
		return plan, creator, err
	}
	plan.GraphHash, plan.Untracked = valueHash(g), g.Untracked
	plan.GraphSnapshot = &g
	plan.Items = domain.DependencyImpact(plan.Changes, g.Links, g.Frozen)
	affected := map[string]bool{}
	for _, change := range plan.Changes {
		affected[change.Before.Key()], affected[change.After.Key()] = true, true
	}
	for _, item := range plan.Items {
		affected[item.Node.Key()] = true
	}
	for _, entry := range g.Entries {
		if entry.Ref.Kind == "resource" && !affected[entry.Ref.Key()] {
			plan.OutsideRecordedClosure = append(plan.OutsideRecordedClosure, entry.Ref)
		}
	}
	return plan, creator, nil
}

func publicationSnapshotChanges(before, after []DependencyEntry) []domain.DependencyChange {
	refs := map[string]domain.DependencyChange{}
	for _, entry := range before {
		refs[entry.Ref.Key()] = domain.DependencyChange{Before: entry.Ref}
	}
	for _, entry := range after {
		change := refs[entry.Ref.Key()]
		change.After = entry.Ref
		refs[entry.Ref.Key()] = change
	}
	changes := []domain.DependencyChange{}
	for _, change := range refs {
		if change.Before.Hash != change.After.Hash {
			changes = append(changes, change)
		}
	}
	slices.SortFunc(changes, func(a, b domain.DependencyChange) int {
		return strings.Compare(a.Before.Key()+a.After.Key(), b.Before.Key()+b.After.Key())
	})
	return changes
}
