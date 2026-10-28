package service

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"sort"
	"strings"
	"unicode/utf8"
)

type RepairInput struct {
	Decisions []domain.RepairDependencyDecision `json:"decisions,omitempty"`
}

func repairDecisionKey(resource string, source domain.DependencyRef, kind string) string {
	return resource + "\x00" + source.Key() + "\x00" + kind
}

func normalizeRepairInput(input RepairInput) (RepairInput, error) {
	if len(input.Decisions) > 128 {
		return input, domain.ErrValidation
	}
	input.Decisions = append([]domain.RepairDependencyDecision(nil), input.Decisions...)
	seen := map[string]bool{}
	for _, decision := range input.Decisions {
		key := repairDecisionKey(decision.ResourceID, decision.Source, decision.Kind)
		if seen[key] || (decision.Action != "KEEP_HISTORY" && decision.Action != "DROP_REFERENCE" && decision.Action != "KEEP_CURRENT") || strings.TrimSpace(decision.Reason) == "" || !utf8.ValidString(decision.Reason) || utf8.RuneCountInString(decision.Reason) > 400 {
			return input, domain.ErrValidation
		}
		seen[key] = true
	}
	sort.Slice(input.Decisions, func(i, j int) bool {
		a, b := input.Decisions[i], input.Decisions[j]
		return repairDecisionKey(a.ResourceID, a.Source, a.Kind) < repairDecisionKey(b.ResourceID, b.Source, b.Kind)
	})
	return input, nil
}

func repairOperation(id string, input RepairInput) string {
	if len(input.Decisions) == 0 {
		return "continuity-repair-v1:" + id
	}
	raw, _ := json.Marshal(input)
	return "continuity-decision-v1:" + id + ":" + digest(string(raw))[:24]
}

// Author decisions are real versioned dependency edits in the existing release,
// not a second mutable status ledger. Their generator and plan remain in origin.
func (s *Generations) applyRepairDecisions(ctx context.Context, actor domain.Actor, project string, plan ChangePlan, repair domain.ContinuityRepair, updates []domain.ResourceUpdate, input RepairInput) (domain.ContinuityRepair, []domain.ResourceUpdate, error) {
	positions := map[string]int{}
	for i, update := range updates {
		positions[update.ResourceID] = i
	}
	affected := map[string]bool{}
	deleted := map[string]bool{}
	for _, change := range plan.Changes {
		affected[change.Before.Key()] = true
		if change.Before.ID != "" && change.After.ID == "" {
			deleted[change.Before.Key()] = true
		}
	}
	for _, item := range plan.Items {
		for _, via := range item.Via {
			affected[via.Source.Key()] = true
		}
	}
	options := map[string]domain.RepairDependencyOption{}
	resources := map[string]domain.Resource{}
	for _, item := range plan.Items {
		if item.Node.Kind != "resource" || item.Node.BranchID != repair.BranchID {
			continue
		}
		r, err := s.IP.ReadResource(ctx, actor, project, item.Node.ID)
		if err != nil {
			return repair, nil, err
		}
		if r.Head != item.Node.Revision || resourceNodeHash(r.Hash, r.Policy, r.Dependencies) != item.Node.Hash {
			return repair, nil, domain.ErrConflict
		}
		resources[r.ID] = r
		policy := r.Dependencies
		if i, exists := positions[r.ID]; exists {
			policy = updates[i].Dependencies
		}
		if policy == nil {
			continue
		}
		for _, dependency := range policy.Inputs {
			if !affected[dependency.Source.Key()] {
				continue
			}
			option := domain.RepairDependencyOption{ResourceID: r.ID, Name: r.Name, Source: dependency.Source, Kind: dependency.Kind, Deleted: deleted[dependency.Source.Key()]}
			options[repairDecisionKey(r.ID, dependency.Source, dependency.Kind)] = option
			repair.DependencyOptions = append(repair.DependencyOptions, option)
		}
	}
	permission, err := s.IP.Workspace.Project(ctx, actor, project)
	if err != nil {
		return repair, nil, err
	}
	for _, decision := range input.Decisions {
		option, ok := options[repairDecisionKey(decision.ResourceID, decision.Source, decision.Kind)]
		if !ok || option.Source != decision.Source {
			return repair, nil, domain.ErrConflict
		}
		if decision.Action == "KEEP_CURRENT" && option.Deleted {
			return repair, nil, domain.ErrValidation
		}
		r := resources[decision.ResourceID]
		if r.Dependencies != nil && r.Dependencies.Frozen && permission.Role != "owner" {
			return repair, nil, domain.ErrForbidden
		}
		i, exists := positions[r.ID]
		if !exists {
			i = len(updates)
			updates = append(updates, domain.ResourceUpdate{ResourceID: r.ID, ExpectedHead: r.Head, Text: r.Text, Policy: r.Policy, Dependencies: r.Dependencies, Origin: &domain.ArtifactOriginInput{GenerationID: repair.GenerationID}})
			positions[r.ID] = i
		}
		update := &updates[i]
		if update.Origin == nil {
			update.Origin = &domain.ArtifactOriginInput{}
		}
		update.Origin.GenerationID = repair.GenerationID
		if update.SourceImportID != "" {
			update.SourceImportID = ""
			update.PartialAcknowledged = false
		}
		var next domain.DependencyPolicy
		raw, _ := json.Marshal(update.Dependencies)
		if json.Unmarshal(raw, &next) != nil {
			return repair, nil, domain.ErrNotReady
		}
		found := false
		for j, dependency := range next.Inputs {
			if dependency.Source != decision.Source || dependency.Kind != decision.Kind {
				continue
			}
			found = true
			switch decision.Action {
			case "DROP_REFERENCE":
				next.Inputs = append(next.Inputs[:j], next.Inputs[j+1:]...)
			case "KEEP_HISTORY":
				next.Inputs[j].Pinned = true
				next.Inputs[j].Reason = "作者决定保留历史引用：" + decision.Reason
			case "KEEP_CURRENT":
				next.Inputs[j].Pinned = false
				next.Inputs[j].Reason = "作者决定继续使用：" + decision.Reason
			}
			break
		}
		if !found {
			return repair, nil, domain.ErrConflict
		}
		update.Dependencies = &next
		label := map[string]string{"KEEP_HISTORY": "保留历史引用", "DROP_REFERENCE": "解除引用", "KEEP_CURRENT": "继续使用当前资料"}[decision.Action]
		entry := "变更处理（" + label + "，" + decision.Source.Kind + "/" + decision.Source.ID + "）：" + decision.Reason
		if update.FrozenChangeReason != "" {
			update.FrozenChangeReason += "\n"
		}
		update.FrozenChangeReason += entry
		if utf8.RuneCountInString(update.FrozenChangeReason) > 2000 {
			return repair, nil, domain.ErrValidation
		}
	}
	if len(updates) > 32 {
		return repair, nil, domain.ErrValidation
	}
	for _, update := range updates {
		if update.Dependencies == nil {
			continue
		}
		for _, dependency := range update.Dependencies.Inputs {
			if !dependency.Pinned && deleted[dependency.Source.Key()] && repair.SaveBlockedReason == "" {
				repair.SaveBlockedReason = "存在已删除来源的跟随引用。请为对应内容选择保留历史引用或解除引用，填写理由后检查处理决定。"
			}
		}
	}
	repair.Decisions = input.Decisions
	return repair, updates, nil
}
