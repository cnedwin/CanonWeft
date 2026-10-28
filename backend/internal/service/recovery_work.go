package service

import (
	"encoding/json"
	"slices"
	"strings"

	"novelstudio/internal/domain"
)

func remapRecoveryField(value *string, ids map[string]string) error {
	mapped, err := recoveredID(ids, *value)
	if err == nil {
		*value = mapped
	}
	return err
}

func remapRecoveryPatchGroup(group *domain.TextPatchGroup, ids RecoveryResourceIDs) error {
	group.ID = digest("recovery-group:" + ids.TargetProject + ":" + group.ID)[:32]
	if err := remapRecoveryField(&group.CompensatesReleaseID, ids.Releases); err != nil {
		return err
	}
	for i := range group.Members {
		if err := remapRecoveryField(&group.Members[i], ids.Resources); err != nil {
			return err
		}
	}
	for i := range group.Patches {
		patch := &group.Patches[i]
		patch.ID = digest("recovery-patch:" + ids.TargetProject + ":" + patch.ID)[:32]
		patch.GroupID = group.ID
		if err := remapRecoveryField(&patch.ResourceID, ids.Resources); err != nil {
			return err
		}
		if err := remapRecoveryField(&patch.BaseRevision, ids.Releases); err != nil {
			return err
		}
	}
	return nil
}

// Remap only typed identities. Author snippets, labels, offsets and reasons
// remain byte-for-byte unchanged, including incomplete and stale drafts.
func remapRecoveryWork(data map[string][]recoveryRow, original, copied map[string]domain.Manifest, ids RecoveryResourceIDs, expiry *recoveryExpiryRows) (map[string]string, error) {
	plans := map[string]string{}
	links := map[string]string{}
	for _, table := range recoveryResourceTables {
		if table.name == "change_plan_releases" {
			for _, row := range data[table.name] {
				links[table.value(row, "plan_id")] = table.value(row, "release_id")
			}
		}
	}
	for _, table := range recoveryResourceTables {
		for _, row := range data[table.name] {
			if table.name != "patch_drafts" && table.name != "change_plans" {
				continue
			}
			if expiry.expired(table.name, row, table, "payload") {
				if table.name == "change_plans" {
					plans[table.value(row, "id")] = NewID()
				}
				continue
			}
			raw := []byte(table.value(row, "payload"))
			if err := rejectExpiredRuntime(raw, table.name+".payload"); err != nil {
				return nil, err
			}
			var next any
			if table.name == "patch_drafts" {
				var draft domain.PatchDraftContent
				if digest(string(raw)) != table.value(row, "content_hash") || decodeRecoveryDocument(raw, &draft) != nil || draft.Validate() != nil {
					return nil, domain.ErrNotReady
				}
				if err := remapRecoveryField(&draft.BaseIPHead, ids.Revisions); err != nil {
					return nil, err
				}
				if err := remapRecoveryField(&draft.SelectedResourceID, ids.Resources); err != nil {
					return nil, err
				}
				if err := remapRecoveryField(&draft.CompensationSourceID, ids.Releases); err != nil {
					return nil, err
				}
				if err := remapRecoveryPatchGroup(&draft.Group, ids); err != nil {
					return nil, err
				}
				draft.OperationID = "recovery:" + ids.TargetProject + ":" + draft.Group.ID
				if draft.Validate() != nil {
					return nil, domain.ErrNotReady
				}
				next = draft
			} else {
				var plan domain.ChangePlan
				oldID := table.value(row, "id")
				if decodeRecoveryDocument(raw, &plan) != nil || valueHash(plan) != table.value(row, "content_hash") || plan.ID != oldID || plan.BranchID != table.value(row, "branch_id") || plan.CandidateID != table.value(row, "candidate_id") || plan.ReleaseID != links[oldID] {
					return nil, domain.ErrNotReady
				}
				if err := remapRecoveryPlan(&plan, original, copied, ids); err != nil {
					return nil, err
				}
				plans[oldID] = plan.ID
				next = plan
			}
			encoded, err := json.Marshal(next)
			if err != nil {
				return nil, err
			}
			table.set(row, "payload", string(encoded))
			table.set(row, "content_hash", digest(string(encoded)))
		}
	}
	if !validRecoveryIDs(plans) {
		return nil, domain.ErrConflict
	}
	return plans, nil
}

func remapRecoveryPlan(plan *domain.ChangePlan, original, copied map[string]domain.Manifest, ids RecoveryResourceIDs) error {
	if len(plan.Recovery) >= 128 {
		return domain.ErrNotReady
	}
	proof := domain.ChangePlanRecovery{ProjectID: ids.SourceProject, PlanID: plan.ID, ContentHash: valueHash(*plan), SourceEventID: plan.SourceEventID, SourceEventKind: plan.SourceEventKind}
	plan.Recovery = append(plan.Recovery, proof)
	plan.ID = digest("recovery-plan:" + ids.TargetProject + ":" + plan.ID)[:32]
	// Events and review grants remain in the old project; the copy is author work.
	plan.SourceEventID, plan.SourceEventKind, plan.SemanticReview = "", "", "NOT_RUN"
	for _, field := range []struct {
		value   *string
		mapping map[string]string
	}{
		{&plan.BranchID, ids.Branches}, {&plan.CandidateID, ids.Revisions},
		{&plan.BaseHead, ids.Revisions}, {&plan.ReleaseID, ids.Releases},
	} {
		if err := remapRecoveryField(field.value, field.mapping); err != nil {
			return err
		}
	}
	remap := func(ref *domain.DependencyRef) error {
		if *ref == (domain.DependencyRef{}) {
			return nil
		}
		if ref.ID == "" || ref.Revision == "" || len(ref.Hash) != 64 || strings.Trim(ref.Hash, "0123456789abcdef") != "" {
			return domain.ErrNotReady
		}
		switch ref.Kind {
		case "resource":
			found := false
			for i, artifact := range original[ref.Revision].Artifacts {
				if artifact.ResourceID != ref.ID {
					continue
				}
				if original[ref.Revision].BranchID != ref.BranchID || resourceNodeHash(artifact.Hash, artifact.Policy, artifact.Dependencies) != ref.Hash || i >= len(copied[ref.Revision].Artifacts) {
					return domain.ErrNotReady
				}
				copy := copied[ref.Revision].Artifacts[i]
				ref.Hash = resourceNodeHash(copy.Hash, copy.Policy, copy.Dependencies)
				found = true
				break
			}
			if !found {
				return domain.ErrNotReady
			}
			if err := remapRecoveryField(&ref.ID, ids.Resources); err != nil {
				return err
			}
			if err := remapRecoveryField(&ref.Revision, ids.Releases); err != nil {
				return err
			}
		case "skill":
			if ref.BranchID != "" || ref.ID != ref.Revision {
				return domain.ErrNotReady
			}
			if err := remapRecoveryField(&ref.ID, ids.Skills); err != nil {
				return err
			}
			if err := remapRecoveryField(&ref.Revision, ids.Skills); err != nil {
				return err
			}
		case "entity", "assertion", "rule", "event", "mapping":
			if err := remapRecoveryField(&ref.Revision, ids.Revisions); err != nil {
				return err
			}
		default:
			return domain.ErrNotReady
		}
		return remapRecoveryField(&ref.BranchID, ids.Branches)
	}
	remapLinks := func(links []domain.DependencyLink) error {
		for i := range links {
			if err := remap(&links[i].Source); err != nil {
				return err
			}
			if err := remap(&links[i].Target); err != nil {
				return err
			}
		}
		return nil
	}
	for i := range plan.Changes {
		if err := remap(&plan.Changes[i].Before); err != nil {
			return err
		}
		if err := remap(&plan.Changes[i].After); err != nil {
			return err
		}
	}
	slices.SortFunc(plan.Changes, func(a, b domain.DependencyChange) int {
		return strings.Compare(a.Before.Key()+a.After.Key(), b.Before.Key()+b.After.Key())
	})
	for i := range plan.Differences {
		diff := &plan.Differences[i]
		if err := remap(&diff.Ref); err != nil {
			return err
		}
		if diff.Ref.Kind != "resource" {
			continue
		}
		for j := range diff.Fields {
			field := &diff.Fields[j]
			if field.Name != "dependencies" {
				continue
			}
			for _, value := range []*string{&field.Before, &field.After} {
				var policy *domain.DependencyPolicy
				if decodeRecoveryDocument([]byte(*value), &policy) != nil {
					return domain.ErrNotReady
				}
				if policy != nil {
					if policy.Validate() != nil {
						return domain.ErrNotReady
					}
					for k := range policy.Inputs {
						if err := remap(&policy.Inputs[k].Source); err != nil {
							return err
						}
					}
				}
				raw, _ := json.Marshal(policy)
				*value = string(raw)
			}
		}
	}
	for _, refs := range [][]domain.DependencyRef{plan.Untracked, plan.OutsideRecordedClosure} {
		for i := range refs {
			if err := remap(&refs[i]); err != nil {
				return err
			}
		}
	}
	for i := range plan.Items {
		if err := remap(&plan.Items[i].Node); err != nil {
			return err
		}
		if err := remapLinks(plan.Items[i].Via); err != nil {
			return err
		}
	}
	for i := range plan.AdditionalReview {
		if err := remap(&plan.AdditionalReview[i].Node); err != nil {
			return err
		}
	}
	if graph := plan.GraphSnapshot; graph != nil {
		if valueHash(graph) != plan.GraphHash {
			return domain.ErrNotReady
		}
		frozen := map[string]bool{}
		seen := map[string]bool{}
		for i := range graph.Entries {
			ref := &graph.Entries[i].Ref
			key := ref.Key()
			if seen[key] {
				return domain.ErrNotReady
			}
			seen[key] = true
			if err := remap(ref); err != nil {
				return err
			}
			if value, ok := graph.Frozen[key]; ok {
				frozen[ref.Key()] = value
			}
		}
		if len(frozen) != len(graph.Frozen) {
			return domain.ErrNotReady
		}
		graph.Frozen = frozen
		if err := remapLinks(graph.Links); err != nil {
			return err
		}
		for i := range graph.Untracked {
			if err := remap(&graph.Untracked[i]); err != nil {
				return err
			}
		}
		plan.GraphHash = valueHash(graph)
		// Impact ordering depends on identity keys; recompute against this exact
		// historical graph, never today's resource heads.
		items, err := domain.ChangeReviewItems(plan.Changes, *graph, plan.AdditionalReview)
		if err != nil {
			return err
		}
		plan.Items = items
		plan.OutsideRecordedClosure = domain.ChangeOutsideRefs(plan.Changes, items, *graph)
	} else {
		// Legacy digest-only plans cannot be made current by fabricating a graph.
		plan.Stale, plan.SourceState = true, "SUPERSEDED"
	}
	return nil
}
