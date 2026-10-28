package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"novelstudio/internal/domain"
)

// RefreshPublicationPlan reuses the exact published source but captures a new
// target watermark. The old immutable plan and its decisions remain history.
func (s *IP) RefreshPublicationPlan(ctx context.Context, actor domain.Actor, project, id, operation string) (ChangePlan, error) {
	var empty ChangePlan
	if !validName(operation, 120) {
		return empty, domain.ErrValidation
	}
	tx, a, _, err := s.begin(ctx, actor, project, true)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	fingerprint := digest("change.refresh\x00" + project + "\x00" + id)
	prior, result, receiptErr := operationReceiptTx(ctx, tx, a, operation)
	if receiptErr == nil {
		if prior != fingerprint {
			return empty, domain.ErrConflict
		}
		return readChangePlanTx(ctx, tx, project, result)
	}
	if receiptErr != sql.ErrNoRows {
		return empty, receiptErr
	}
	old, err := readChangePlanTx(ctx, tx, project, id)
	if err != nil {
		return empty, err
	}
	if old.SourceEventID == "" {
		return empty, domain.ErrValidation
	}
	source := old.CandidateID
	if old.SourceEventKind == "release.published" {
		source = old.ReleaseID
	}
	plan, _, err := s.publicationPlanTx(ctx, tx, project, source, old.SourceEventKind)
	if err != nil {
		return empty, err
	}
	if plan.SourceState != "CURRENT" {
		return empty, domain.ErrConflict
	}
	if err = refreshAdditionalChangeScope(&plan, old); err != nil {
		return empty, err
	}
	plan.ID, plan.SourceEventID, plan.SourceEventKind = NewID(), old.SourceEventID, old.SourceEventKind
	raw, _ := json.Marshal(plan)
	var candidate any
	if plan.CandidateID != "" {
		candidate = plan.CandidateID
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO change_plans(id,project_id,branch_id,candidate_id,payload,content_hash,created_by) VALUES(?,?,?,?,?,?,?)", plan.ID, project, plan.BranchID, candidate, raw, valueHash(plan), a.ID); err != nil {
		return empty, err
	}
	if plan.ReleaseID != "" {
		if _, err = tx.ExecContext(ctx, "INSERT INTO change_plan_releases(plan_id,release_id) VALUES(?,?)", plan.ID, plan.ReleaseID); err != nil {
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'change.plan',?,?)", a.ID, operation, fingerprint, plan.ID); err != nil {
		return empty, err
	}
	if err = audit(ctx, tx, a, "change.plan", plan.ID); err != nil {
		return empty, err
	}
	if err = s.captureChangePlanSourceTx(ctx, tx, project, plan.ID); err != nil {
		return empty, err
	}
	return plan, tx.Commit()
}

type ChangeOutcome struct {
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	BranchID     string `json:"branch_id"`
	ReleaseID    string `json:"release_id"`
	GenerationID string `json:"generation_id"`
	Status       string `json:"status"`
	Current      bool   `json:"current"`
	Reason       string `json:"reason"`
}
type ChangeOutcomes struct {
	Next      string          `json:"next,omitempty"`
	Items     []ChangeOutcome `json:"items"`
	Truncated bool            `json:"truncated"`
	PlanStale bool            `json:"plan_stale"`
	Untracked int             `json:"untracked"`
	Outside   int             `json:"outside"`
}

// Outcomes are derived from real release versions and generation ancestry, not
// client status flags. Same-event refreshes can show still-current published
// results without treating older targets or unrelated events as resolved.
func (s *IP) ChangeOutcomes(ctx context.Context, actor domain.Actor, project, id string) (ChangeOutcomes, error) {
	return s.ChangeOutcomesPage(ctx, actor, project, id, "", 100)
}

// Cursor pagination lists live per-version records, not a frozen completeness
// certificate. Restart at page one to include newly created candidates.
func (s *IP) ChangeOutcomesPage(ctx context.Context, actor domain.Actor, project, id, after string, limit int) (ChangeOutcomes, error) {
	result := ChangeOutcomes{Items: []ChangeOutcome{}}
	if limit < 1 || limit > 100 {
		return result, domain.ErrValidation
	}
	if after != "" {
		if _, err := hex.DecodeString(after); err != nil || len(after) != 32 {
			return result, domain.ErrValidation
		}
	}
	plan, err := s.ReadChangePlan(ctx, actor, project, id)
	if err != nil {
		return result, err
	}
	result.PlanStale, result.Untracked, result.Outside = plan.Stale, len(plan.Untracked), len(plan.OutsideRecordedClosure)
	tx, _, _, err := s.begin(ctx, actor, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT r.id FROM artifact_releases r
 JOIN JSON_TABLE(r.manifest,'$.artifacts[*]' COLUMNS(generation_id VARCHAR(32) PATH '$.origin.generation.generation_id' NULL ON EMPTY)) refs
 JOIN generation_tasks g ON g.id=refs.generation_id AND g.project_id=r.project_id
 JOIN change_plans p ON p.id=JSON_UNQUOTE(JSON_EXTRACT(g.frozen,'$.change_plan_id')) AND p.project_id=r.project_id
 WHERE r.project_id=? AND (p.id=? OR (?<>'' AND JSON_UNQUOTE(JSON_EXTRACT(p.payload,'$.source_event_id'))=?))
 AND (?='' OR r.id<?) ORDER BY r.id DESC LIMIT ?`, project, id, plan.SourceEventID, plan.SourceEventID, after, after, limit+1)
	if err != nil {
		return result, err
	}
	ids := []string{}
	for rows.Next() {
		var releaseID string
		if err = rows.Scan(&releaseID); err != nil {
			rows.Close()
			return result, err
		}
		ids = append(ids, releaseID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	if len(ids) > limit {
		result.Truncated = true
		ids = ids[:limit]
		result.Next = ids[len(ids)-1]
	}
	for _, releaseID := range ids {
		r, err := releaseTx(ctx, tx, project, releaseID)
		if err != nil {
			return result, err
		}
		for _, ref := range r.Manifest.Artifacts {
			if ref.Origin == nil || ref.Origin.Generation == nil {
				continue
			}
			g, err := generationTx(ctx, tx, project, ref.Origin.Generation.GenerationID)
			if err != nil {
				return result, err
			}
			if g.Frozen.ChangePlanID == "" {
				continue
			}
			source, err := readChangePlanTx(ctx, tx, project, g.Frozen.ChangePlanID)
			if err != nil {
				return result, err
			}
			if source.ID != plan.ID && (plan.SourceEventID == "" || source.SourceEventID != plan.SourceEventID || valueHash(source.Changes) != valueHash(plan.Changes)) {
				continue
			}
			resource, err := resourceTx(ctx, tx, project, ref.ResourceID)
			if err != nil {
				return result, err
			}
			result.Items = append(result.Items, ChangeOutcome{ResourceID: ref.ResourceID, Name: resource.Name, BranchID: r.Manifest.BranchID, ReleaseID: r.ID, GenerationID: g.ID, Status: r.Status, Current: r.Status == "PUBLISHED" && resource.Head == r.ID, Reason: ref.FrozenChangeReason})
		}
	}
	return result, nil
}
