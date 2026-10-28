package service

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"sort"
)

// composeRepair retains every artifact from the selected unpublished release.
// Text overlap returns an inspectable conflict and no saveable partial result.
func (s *Generations) composeRepair(ctx context.Context, actor domain.Actor, project string, plan ChangePlan, repair domain.ContinuityRepair) (domain.ContinuityRepair, []domain.ResourceUpdate, error) {
	updates := map[string]domain.ResourceUpdate{}
	bases := map[string]domain.Resource{}
	ai := map[string]domain.PatchedResource{}
	for _, item := range repair.Items {
		resource, err := s.IP.ReadResource(ctx, actor, project, item.Target.ResourceID)
		if err != nil {
			return repair, nil, err
		}
		if resource.Head != item.Target.Revision || resource.Hash != item.Target.Hash {
			return repair, nil, domain.ErrConflict
		}
		bases[resource.ID], ai[resource.ID] = resource, item
		updates[resource.ID] = domain.ResourceUpdate{ResourceID: resource.ID, ExpectedHead: resource.Head, Text: item.Result.Text, Policy: resource.Policy, Dependencies: resource.Dependencies, FrozenChangeReason: item.Result.Applied[0].Reason, Origin: &domain.ArtifactOriginInput{GenerationID: repair.GenerationID}}
	}
	if plan.ReleaseID != "" && plan.SourceEventID == "" {
		original, err := s.IP.ReadRelease(ctx, actor, project, plan.ReleaseID)
		if err != nil {
			return repair, nil, err
		}
		if (original.Status != "PREPARED" && original.Status != "READY") || original.Manifest.BranchID != plan.BranchID || original.Manifest.ExpectedIPHead != plan.BaseHead {
			return repair, nil, domain.ErrConflict
		}
		repair.SourceReleaseID = original.ID
		for i, ref := range original.Manifest.Artifacts {
			resource, err := s.IP.ReadResource(ctx, actor, project, ref.ResourceID)
			if err != nil {
				return repair, nil, err
			}
			if resource.Head != ref.ExpectedHead || i >= len(original.Contents) {
				return repair, nil, domain.ErrConflict
			}
			text := original.Contents[i].Text
			if digest(text) != ref.Hash || int64(len(text)) != ref.Size {
				return repair, nil, domain.ErrNotReady
			}
			bases[resource.ID] = resource
			update := domain.ResourceUpdate{ResourceID: ref.ResourceID, ExpectedHead: ref.ExpectedHead, Text: text, Policy: ref.Policy, Dependencies: ref.Dependencies, FrozenChangeReason: ref.FrozenChangeReason, SourceImportID: ref.SourceImportID, PartialAcknowledged: ref.PartialAcknowledged, Origin: &domain.ArtifactOriginInput{ParentReleaseID: original.ID}}
			item, overlap := ai[resource.ID]
			if overlap {
				merged, mergeErr := domain.MergeContinuityRepair(item.Target, text, item.Result.Applied)
				if errors.Is(mergeErr, domain.ErrConflict) {
					repair.Conflicts = append(repair.Conflicts, domain.RepairConflict{ResourceID: resource.ID, Name: resource.Name, Original: resource.Text, Proposed: text, AIText: item.Result.Text})
					repair.SaveBlockedReason = "原候选与 AI 的改动范围相交或无法安全分离，不能自动选择。请在联合发布列表编辑原候选解决冲突，再基于新候选重新生成影响清单与修订。"
				} else if mergeErr != nil {
					return repair, nil, mergeErr
				} else {
					update.Text = merged
					update.Origin.GenerationID = repair.GenerationID
					// The immutable parent retains the original import attribution;
					// derived text is not falsely presented as an untouched import.
					update.SourceImportID = ""
					update.PartialAcknowledged = false
					update.FrozenChangeReason = item.Result.Applied[0].Reason
				}
			}
			updates[resource.ID] = update
			repair.Preserved = append(repair.Preserved, domain.RepairPreservedArtifact{ResourceID: resource.ID, Name: resource.Name, Text: update.Text, Merged: overlap})
		}
	}
	if len(updates) > 32 {
		return repair, nil, domain.ErrValidation
	}
	ids := make([]string, 0, len(updates))
	for id := range updates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]domain.ResourceUpdate, 0, len(ids))
	for _, id := range ids {
		result = append(result, updates[id])
	}
	if len(repair.Conflicts) > 0 {
		return repair, nil, nil
	}
	if repair.SourceReleaseID == "" {
		return repair, result, nil
	}
	// Keep a single declared group for the original changes plus new repairs.
	// New resources and policy-only changes remain additional manifest members.
	group := domain.TextPatchGroup{ID: repair.Group.ID, Members: []string{}, Patches: []domain.TextPatch{}}
	targets := []domain.PatchTarget{}
	for _, id := range ids {
		base, update := bases[id], updates[id]
		if base.Head == "" || base.Text == update.Text {
			continue
		}
		target := domain.PatchTarget{ResourceID: id, Revision: base.Head, Hash: base.Hash, Text: base.Text}
		group.Members = append(group.Members, id)
		targets = append(targets, target)
		// Reuse the conservative difference bound to anchor the final combined
		// text to its real published base, never to an invented intermediate head.
		edit, err := domain.PlanTextCompensation(update.Text, base.Text)
		if err != nil {
			return repair, nil, err
		}
		reason := update.FrozenChangeReason
		if reason == "" {
			reason = "保留原联合候选中的修改并合并局部修订"
		}
		group.Patches = append(group.Patches, domain.TextPatch{ID: digest(group.ID + "\x00combined\x00" + id)[:32], GroupID: group.ID, ResourceID: id, BaseRevision: base.Head, BaseHash: base.Hash, StartRune: edit.StartRune, EndRune: edit.EndRune, ExpectedSliceHash: digest(edit.Expected), Replacement: edit.Replacement, Reason: reason})
	}
	if len(group.Members) == 0 {
		repair.Group, repair.Items = group, []domain.PatchedResource{}
		return repair, result, nil
	}
	items, err := domain.ApplyTextPatchGroup(targets, group)
	if err != nil {
		return repair, nil, err
	}
	repair.Group, repair.Items = group, items
	return repair, result, nil
}
