package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"slices"
	"strings"
)

type ChangeScopeInput struct {
	OperationID string                     `json:"operation_id"`
	Targets     []domain.ChangeReviewScope `json:"targets"`
}

// ExtendChangeScope creates an immutable new plan. The target bytes and graph
// must still match; no canonical dependency or content is modified here.
func (s *IP) ExtendChangeScope(ctx context.Context, actor domain.Actor, project, id string, input ChangeScopeInput) (ChangePlan, error) {
	var empty ChangePlan
	if !validName(input.OperationID, 120) || len(input.Targets) == 0 || len(input.Targets) > 128 {
		return empty, domain.ErrValidation
	}
	input.Targets = slices.Clone(input.Targets)
	for i := range input.Targets {
		input.Targets[i].Reason = strings.TrimSpace(input.Targets[i].Reason)
	}
	slices.SortFunc(input.Targets, func(a, b domain.ChangeReviewScope) int { return strings.Compare(a.Node.Key(), b.Node.Key()) })
	fingerprint := valueHash(struct {
		Project, Plan string
		Targets       []domain.ChangeReviewScope
	}{project, id, input.Targets})
	tx, a, _, err := s.begin(ctx, actor, project, true)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	prior, result, receiptErr := operationReceiptTx(ctx, tx, a, input.OperationID)
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
	plan, err := currentChangePlanTx(ctx, tx, project, old.BranchID, id)
	if err != nil {
		return empty, err
	}
	if plan.GraphSnapshot == nil {
		return empty, domain.ErrNotReady
	}
	plan.AdditionalReview = append(slices.Clone(plan.AdditionalReview), input.Targets...)
	slices.SortFunc(plan.AdditionalReview, func(a, b domain.ChangeReviewScope) int { return strings.Compare(a.Node.Key(), b.Node.Key()) })
	if err = applyAdditionalChangeScope(&plan); err != nil {
		return empty, err
	}
	plan.ID, plan.Stale = NewID(), false
	raw, _ := json.Marshal(plan)
	var candidate any
	if plan.CandidateID != "" {
		candidate = plan.CandidateID
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO change_plans(id,project_id,branch_id,candidate_id,payload,content_hash,created_by) VALUES(?,?,?,?,?,?,?)", plan.ID, project, plan.BranchID, candidate, raw, valueHash(plan), a.ID); err != nil {
		return empty, err
	}
	if plan.ReleaseID != "" {
		if _, err = tx.ExecContext(ctx, txSQLChangePlanRelease, plan.ID, plan.ReleaseID); err != nil {
			return empty, err
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'change.plan',?,?)", a.ID, input.OperationID, fingerprint, plan.ID); err != nil {
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

const txSQLChangePlanRelease = "INSERT INTO change_plan_releases(plan_id,release_id) VALUES(?,?)"

func applyAdditionalChangeScope(plan *ChangePlan) error {
	if plan.GraphSnapshot == nil {
		return domain.ErrNotReady
	}
	items, err := domain.ChangeReviewItems(plan.Changes, *plan.GraphSnapshot, plan.AdditionalReview)
	if err != nil {
		return err
	}
	plan.Items = items
	plan.OutsideRecordedClosure = domain.ChangeOutsideRefs(plan.Changes, items, *plan.GraphSnapshot)
	// Inclusion does not perform independent semantic review.
	plan.SemanticReview = "NOT_RUN"
	return nil
}

func refreshAdditionalChangeScope(plan *ChangePlan, old ChangePlan) error {
	if len(old.AdditionalReview) == 0 {
		return nil
	}
	if plan.GraphSnapshot == nil {
		return domain.ErrNotReady
	}
	entries := map[string]domain.DependencyRef{}
	for _, entry := range plan.GraphSnapshot.Entries {
		entries[entry.Ref.Key()] = entry.Ref
	}
	for _, choice := range old.AdditionalReview {
		current, ok := entries[choice.Node.Key()]
		if !ok {
			return domain.ErrConflict
		}
		plan.AdditionalReview = append(plan.AdditionalReview, domain.ChangeReviewScope{Node: current, Reason: choice.Reason})
	}
	return applyAdditionalChangeScope(plan)
}
