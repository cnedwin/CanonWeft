package service

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"slices"
)

// Queue state is reconstructed from durable heads and immutable history. It is
// not a second authority for adoption, model execution, or spending.
type MemoryRefreshTask struct {
	ID     string                `json:"id"`
	State  string                `json:"state"`
	Reason string                `json:"reason"`
	Input  ExtractionSourceInput `json:"input"`
}

func (s *Memory) refreshTasksTx(ctx context.Context, tx *sql.Tx, project, branch string, items []memory.Item, memo map[string]memorySourceInfo) ([]MemoryRefreshTask, error) {
	tasks := []MemoryRefreshTask{}
	byID := map[string]memory.Item{}
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, item := range items {
		if item.Summary == nil || (item.State != "stale" && item.State != "candidate") {
			continue
		}
		timeline := item.Timeline
		if timeline == "" {
			timeline = "main"
		}
		task := MemoryRefreshTask{ID: item.ID + ":" + item.Revision, State: "READY", Input: ExtractionSourceInput{SummaryTarget: &SummaryTarget{ItemID: item.ID, Revision: item.Revision, Level: item.Summary.Level, UnitID: item.Summary.UnitID, Title: item.Summary.Title, Timeline: timeline}, StoryTime: max(item.From, item.RevealAt), Focus: []string{"summaries", "theme", "highlights", "foreshadow"}}}
		task.Input.ResourceIDs, task.Input.MemoryIDs = refreshSourceIDs(item)
		if item.State == "candidate" {
			task.State = "REVIEW"
			task.Reason = "先审阅并采用当前候选"
		} else {
			for _, id := range task.Input.MemoryIDs {
				child, ok := byID[id]
				if !ok || (child.State != "published" && child.State != "adopted_plan") {
					task.State = "WAITING_INPUT"
					task.Reason = "下层摘要尚未重新确认或不可用"
					continue
				}
				task.Input.StoryTime = max(task.Input.StoryTime, max(child.From, child.RevealAt))
			}
			for _, id := range task.Input.ResourceIDs {
				info, loaded := memo[id]
				if !loaded {
					resource, err := resourceTx(ctx, tx, project, id)
					if err != nil {
						if errors.Is(err, domain.ErrForbidden) {
							task.State = "WAITING_INPUT"
							task.Reason = "原始资料不可用"
							continue
						}
						return nil, err
					}
					info.Resource = resource
				}
				if info.Resource.Head == "" || info.Resource.BranchID != branch {
					task.State = "WAITING_INPUT"
					task.Reason = "原始资料尚未发布"
				}
				if info.Resource.Policy != nil {
					task.Input.StoryTime = max(task.Input.StoryTime, info.Resource.Policy.RevealAt)
				}
			}
		}
		if item.Save != "" || (item.Until != nil && task.Input.StoryTime >= *item.Until) {
			task.State = "WAITING_INPUT"
			task.Reason = "当前存档或有效期需要人工调整"
		}
		tasks = append(tasks, task)
	}
	slices.SortFunc(tasks, func(a, b MemoryRefreshTask) int {
		if n := cmp.Compare(memory.SummaryRank(a.Input.SummaryTarget.Level), memory.SummaryRank(b.Input.SummaryTarget.Level)); n != 0 {
			return n
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return tasks, nil
}

func (s *Memory) refreshTargetTx(ctx context.Context, tx *sql.Tx, actor, project, branch string, target *SummaryTarget) (*memory.Item, error) {
	if target == nil || (target.ItemID == "" && target.Revision == "") {
		return nil, nil
	}
	if !lowerHex32(target.ItemID) || !lowerHex32(target.Revision) {
		return nil, domain.ErrValidation
	}
	item, err := memoryItemTx(ctx, tx, actor, project, branch, target.ItemID)
	if err != nil {
		return nil, err
	}
	if item.Revision != target.Revision || item.State == "revoked" {
		return nil, domain.ErrConflict
	}
	timeline := item.Timeline
	if timeline == "" {
		timeline = "main"
	}
	if item.Summary == nil || item.Kind != "summary" || item.Save != "" || item.Summary.Level != target.Level || item.Summary.UnitID != target.UnitID || item.Summary.Title != target.Title || timeline != target.Timeline {
		return nil, domain.ErrValidation
	}
	return &item, nil
}

// A repair may omit only its own old summary and the summaries derived from it.
// Unrelated hard anchors and explicit dependencies remain mandatory.
func refreshContextItems(items []memory.Item, target string) []memory.Item {
	parents := map[string][]string{}
	for _, item := range items {
		if item.Summary != nil {
			for _, pin := range item.Summary.Inputs {
				parents[pin.ID] = append(parents[pin.ID], item.ID)
			}
		}
	}
	excluded := map[string]bool{target: true}
	queue := []string{target}
	for i := 0; i < len(queue); i++ {
		for _, id := range parents[queue[i]] {
			if !excluded[id] {
				excluded[id] = true
				queue = append(queue, id)
			}
		}
	}
	result := make([]memory.Item, 0, len(items))
	for _, item := range items {
		if !excluded[item.ID] {
			result = append(result, item)
		}
	}
	return result
}

func refreshMatchesStep(scope *memory.RefreshScope, step RecipeStepRun) error {
	if scope == nil {
		return nil
	}
	var input extractionDocument
	if step.Frozen.Agent.Definition.Key != "shared-memory-consolidation" || json.Unmarshal(step.Frozen.Input.Input, &input) != nil || input.SummaryTarget == nil || input.SummaryTarget.ItemID != scope.ID || input.SummaryTarget.Revision != scope.Revision {
		return domain.ErrValidation
	}
	return nil
}

func validateRefreshContextUseTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.ContextID == "" {
		return nil
	}
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT JSON_EXTRACT(payload,'$.memory.request.refresh') FROM context_packages WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?", g.Frozen.ContextID, g.ProjectID, g.BranchID, g.ActorID).Scan(&raw)
	if err != nil {
		return err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var scope memory.RefreshScope
	if json.Unmarshal(raw, &scope) != nil || g.Frozen.RecipeRunID == "" {
		return domain.ErrValidation
	}
	r, err := recipeTx(ctx, tx, g.ProjectID, g.Frozen.RecipeRunID)
	if err != nil {
		return err
	}
	step, err := recipeStepByID(r, g.Frozen.RecipeStepID)
	if err != nil {
		return err
	}
	return refreshMatchesStep(&scope, step)
}

func refreshImportOperation(r RecipeExecution, step RecipeStepRun) string {
	return "memory.refresh-import." + r.ID + "." + step.ID
}
func refreshImportHash(r RecipeExecution, step RecipeStepRun) string {
	return valueHash([]string{r.ProjectID, r.BranchID, r.ID, step.ID, step.OutputHash, digest(string(step.Frozen.Input.Input))})
}
func refreshImportedTx(ctx context.Context, tx *sql.Tx, r RecipeExecution, step RecipeStepRun) (bool, error) {
	if !slices.Contains([]string{"WAITING_CONFIRMATION", "COMPLETED"}, step.State) || step.OutputHash == "" {
		return false, nil
	}
	var doc extractionDocument
	if json.Unmarshal(step.Frozen.Input.Input, &doc) != nil || doc.SummaryTarget == nil || doc.SummaryTarget.ItemID == "" {
		return false, nil
	}
	var hash, id string
	err := tx.QueryRowContext(ctx, "SELECT request_hash,result_id FROM operations WHERE actor_id=? AND operation_id=? AND kind='memory.update'", r.ActorID, refreshImportOperation(r, step)).Scan(&hash, &id)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != refreshImportHash(r, step) || id != doc.SummaryTarget.ItemID {
		return false, domain.ErrConflict
	}
	return true, nil
}

func refreshSourceIDs(item memory.Item) (resources, summaries []string) {
	for _, source := range item.Sources {
		if !slices.Contains(resources, source.Resource) {
			resources = append(resources, source.Resource)
		}
	}
	if item.Summary != nil {
		for _, pin := range item.Summary.Inputs {
			summaries = append(summaries, pin.ID)
		}
	}
	return
}
func sameRefreshIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, id := range a {
		if !slices.Contains(b, id) {
			return false
		}
	}
	return true
}

// A refresh is a new candidate revision of the same summary, not another item
// that would strand every upstream pin on an obsolete identity.
func preserveRefreshPolicy(item memory.Item, prior memory.Item) memory.Item {
	item.ID = prior.ID
	item.Pinned = prior.Pinned
	item.Dependencies = append([]string(nil), prior.Dependencies...)
	item.Visibility = prior.Visibility
	item.KnownBy = append([]string(nil), prior.KnownBy...)
	item.Control = item.Control || prior.Control
	item.From = max(item.From, prior.From)
	item.RevealAt = max(item.RevealAt, prior.RevealAt)
	if prior.Until != nil && (item.Until == nil || *prior.Until < *item.Until) {
		until := *prior.Until
		item.Until = &until
	}
	return item
}
