package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

func (s *Generations) baseRepairPreview(ctx context.Context, actor domain.Actor, project, id string) (domain.ContinuityRepair, error) {
	var empty domain.ContinuityRepair
	g, err := s.Read(ctx, actor, project, id)
	if err != nil {
		return empty, err
	}
	if g.State != "SUCCEEDED" || g.Cancelled || g.Output == nil || g.Frozen.Role != "continuity_editor" || g.Frozen.ChangePlanID == "" {
		return empty, domain.ErrConflict
	}
	source, err := s.IP.changeReviewContext(ctx, actor, project, g.BranchID, g.Frozen.ChangePlanID)
	if err != nil {
		return empty, err
	}
	repair, err := domain.CompileContinuityRepair(g.Output.Text, g.ID, source)
	if err != nil {
		return empty, err
	}
	if source.Plan.CandidateID != "" && source.Plan.SourceEventID == "" {
		tx, _, _, readErr := s.IP.begin(ctx, actor, project, true)
		if readErr != nil {
			return empty, readErr
		}
		candidate, readErr := candidateTx(ctx, tx, g.BranchID, source.Plan.CandidateID)
		tx.Rollback()
		if readErr != nil {
			return empty, readErr
		}
		if candidate.Status != "READY" {
			repair.SaveBlockedReason = "请先在设定候选中确认采用本次设定，再重新预览；保存后设定与正文修订将进入同一联合发布候选。"
		}
	}
	if len(repair.Items) > 0 {
		if _, err = s.IP.PreviewPatches(ctx, actor, project, PatchInput{BranchID: g.BranchID, ExpectedIPHead: g.Frozen.BaseHead, Group: repair.Group}); err != nil {
			return empty, err
		}
	}
	return repair, nil
}

func (s *Generations) PreviewRepair(ctx context.Context, actor domain.Actor, project, id string) (domain.ContinuityRepair, error) {
	return s.PreviewRepairDecisions(ctx, actor, project, id, RepairInput{})
}

func (s *Generations) PreviewRepairDecisions(ctx context.Context, actor domain.Actor, project, id string, input RepairInput) (domain.ContinuityRepair, error) {
	input, err := normalizeRepairInput(input)
	if err != nil {
		return domain.ContinuityRepair{}, err
	}
	repair, err := s.baseRepairPreview(ctx, actor, project, id)
	if err != nil {
		return repair, err
	}
	plan, err := s.IP.ReadChangePlan(ctx, actor, project, repair.PlanID)
	if err != nil {
		return repair, err
	}
	repair, updates, err := s.composeRepair(ctx, actor, project, plan, repair)
	if err != nil {
		return repair, err
	}
	repair, _, err = s.applyRepairDecisions(ctx, actor, project, plan, repair, updates, input)
	return repair, err
}

// Preparing only saves an immutable candidate. Existing release transactions
// recheck the graph, source generation, heads, permissions and review lineage.
func (s *Generations) PrepareRepair(ctx context.Context, actor domain.Actor, project, id string) (domain.Release, error) {
	return s.PrepareRepairDecisions(ctx, actor, project, id, RepairInput{})
}

func (s *Generations) PrepareRepairDecisions(ctx context.Context, actor domain.Actor, project, id string, selection RepairInput) (domain.Release, error) {
	var empty domain.Release
	selection, err := normalizeRepairInput(selection)
	if err != nil {
		return empty, err
	}
	operation := repairOperation(id, selection)
	tx, a, _, err := s.IP.begin(ctx, actor, project, true)
	if err != nil {
		return empty, err
	}
	actor = a
	// A lost reply can be retried even after publication changed current heads.
	_, priorID, priorErr := operationReceiptTx(ctx, tx, a, operation)
	if priorErr == nil {
		defer tx.Rollback()
		g, readErr := generationTx(ctx, tx, project, id)
		if readErr != nil {
			return empty, readErr
		}
		if !canControlGeneration(a, g) {
			return empty, domain.ErrForbidden
		}
		prior, readErr := releaseTx(ctx, tx, project, priorID)
		if readErr != nil {
			return empty, readErr
		}
		if (prior.Manifest.PatchGroup == nil && len(selection.Decisions) == 0) || (prior.Manifest.PatchGroup != nil && prior.Manifest.PatchGroup.ID != domain.ContinuityRepairGroupID(id)) || len(prior.Manifest.Artifacts) == 0 {
			return empty, domain.ErrConflict
		}
		found := false
		for _, ref := range prior.Manifest.Artifacts {
			if ref.Origin != nil && ref.Origin.Generation != nil && ref.Origin.Generation.GenerationID == id {
				found = true
			}
		}
		if !found {
			return empty, domain.ErrConflict
		}
		return prior, nil
	}
	tx.Rollback()
	if priorErr != sql.ErrNoRows {
		return empty, priorErr
	}
	repair, err := s.baseRepairPreview(ctx, actor, project, id)
	if err != nil {
		return empty, err
	}
	if (len(repair.Items) == 0 && len(selection.Decisions) == 0) || repair.SaveBlockedReason != "" {
		return empty, domain.ErrValidation
	}
	plan, err := s.IP.ReadChangePlan(ctx, actor, project, repair.PlanID)
	if err != nil {
		return empty, err
	}
	repair, updates, err := s.composeRepair(ctx, actor, project, plan, repair)
	if err != nil {
		return empty, err
	}
	repair, updates, err = s.applyRepairDecisions(ctx, actor, project, plan, repair, updates, selection)
	if err != nil {
		return empty, err
	}
	if repair.SaveBlockedReason != "" {
		return empty, domain.ErrConflict
	}
	input := domain.ReleaseInput{BranchID: repair.BranchID, ExpectedIPHead: repair.BaseHead, IPCandidateID: plan.CandidateID, OperationID: operation, PatchGroup: &repair.Group}
	if len(repair.Group.Patches) == 0 {
		input.PatchGroup = nil
	}
	if plan.SourceEventID != "" {
		input.IPCandidateID = ""
	}
	currentSources := map[string]domain.DependencyRef{}
	if plan.SourceEventID != "" {
		catalog, readErr := s.IP.DependencyCatalog(ctx, actor, project)
		if readErr != nil {
			return empty, readErr
		}
		for _, entry := range catalog {
			currentSources[entry.Ref.Key()] = entry.Ref
		}
	}
	for _, update := range updates {
		if update.Dependencies != nil {
			for i := range update.Dependencies.Inputs {
				d := &update.Dependencies.Inputs[i]
				if repair.SourceReleaseID != "" && d.Source.Kind == "resource" && d.Source.Revision == repair.SourceReleaseID {
					d.Source.Revision = "$release"
					if !d.Pinned {
						d.Source.Hash = ""
					}
					continue
				}
				if d.Pinned || (d.Source.BranchID != repair.BranchID && d.Source.BranchID != plan.BranchID) {
					continue
				}
				// Follow-latest links move with this explicitly selected repair.
				// Pinned history, unrelated resources and other branches stay exact.
				if plan.SourceEventID != "" {
					latest, exists := currentSources[d.Source.Key()]
					if !exists {
						return empty, domain.ErrConflict
					}
					d.Source = latest
				}
				if input.IPCandidateID != "" && d.Source.BranchID == repair.BranchID && d.Source.Kind != "resource" && d.Source.Kind != "skill" {
					d.Source.Revision, d.Source.Hash = "$candidate", ""
				} else if d.Source.Kind == "resource" {
					for _, member := range updates {
						if d.Source.ID == member.ResourceID {
							d.Source.Revision, d.Source.Hash = "$release", ""
						}
					}
				}
			}
		}
		input.Updates = append(input.Updates, update)
	}
	return retryReleaseSourceAuthority(ctx, func() (domain.Release, error) { return s.IP.prepareRelease(ctx, actor, project, input, plan.ID) })
}
