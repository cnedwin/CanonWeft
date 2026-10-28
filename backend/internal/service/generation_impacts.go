package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

// A plan contains author-only material across branches. It is available only to
// the review/continuity roles, never implicitly injected into character writing.
func validChangePlanRole(role string) bool { return role == "critic" || role == "continuity_editor" }

type ChangeReviewTarget = domain.ChangeReviewTarget
type ChangeReviewContext = domain.ChangeReviewContext

func (s *IP) changeReviewContext(ctx context.Context, a domain.Actor, project, branch, id string, focusIDs ...string) (ChangeReviewContext, error) {
	result := ChangeReviewContext{Targets: []ChangeReviewTarget{}}
	plan, err := s.ReadChangePlan(ctx, a, project, id)
	if err != nil {
		return result, err
	}
	if !changePlanAllowsBranch(plan, branch) {
		return result, domain.ErrForbidden
	}
	if plan.Stale {
		return result, domain.ErrConflict
	}
	result.Plan = plan
	if plan.BranchID != branch {
		focus, readErr := s.ReadBranch(ctx, a, project, branch)
		if readErr != nil {
			return result, readErr
		}
		result.FocusBranchID, result.FocusBaseHead = branch, focus.Head
	}
	refs := []domain.DependencyRef{}
	if len(focusIDs) > 0 {
		refs, err = domain.ChangeInspectionRefs(plan, branch, focusIDs)
		if err != nil {
			return result, err
		}
		result.FocusTargets = refs
	} else {
		for _, item := range plan.Items {
			if item.Node.Kind == "resource" {
				refs = append(refs, item.Node)
			}
		}
	}
	for _, ref := range refs {
		r, err := s.ReadResource(ctx, a, project, ref.ID)
		if err != nil {
			return result, err
		}
		if r.Head != ref.Revision || resourceNodeHash(r.Hash, r.Policy, r.Dependencies) != ref.Hash {
			return result, domain.ErrConflict
		}
		result.Targets = append(result.Targets, ChangeReviewTarget{Ref: ref, Name: r.Name, Text: r.Text, BodyHash: r.Hash, Policy: r.Policy, Dependencies: r.Dependencies})
	}
	return result, nil
}

func currentChangePlanTx(ctx context.Context, tx *sql.Tx, project, branch, id string) (ChangePlan, error) {
	plan, err := readChangePlanTx(ctx, tx, project, id)
	if err != nil {
		return plan, err
	}
	if !changePlanAllowsBranch(plan, branch) {
		return plan, domain.ErrForbidden
	}
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return plan, err
	}
	b, err := branchTx(ctx, tx, project, plan.BranchID)
	if err != nil {
		return plan, err
	}
	graph, err := dependencyGraphTx(ctx, tx, project)
	if err != nil {
		return plan, err
	}
	if plan.SourceState == "SUPERSEDED" || b.Head != plan.BaseHead || valueHash(graph) != plan.GraphHash {
		return plan, domain.ErrConflict
	}
	return plan, nil
}

func changePlanAllowsBranch(plan ChangePlan, branch string) bool {
	if plan.BranchID == branch {
		return true
	}
	if plan.SourceEventID == "" {
		return false
	}
	for _, item := range plan.Items {
		if item.Node.Kind == "resource" && item.Node.BranchID == branch {
			return true
		}
	}
	return false
}
