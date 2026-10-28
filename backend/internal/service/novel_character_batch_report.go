package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type CharacterBatchPass struct {
	Key    string                  `json:"key"`
	IDs    []string                `json:"ids"`
	Result novel.CharacterProposal `json:"result"`
}
type CharacterBatchReport struct {
	Run         RecipeExecution            `json:"run"`
	Passes      []CharacterBatchPass       `json:"passes"`
	Completed   int                        `json:"completed"`
	Total       int                        `json:"total"`
	Stale       bool                       `json:"stale"`
	StaleReason string                     `json:"stale_reason"`
	Suggestion  *CharacterDesignSuggestion `json:"suggestion,omitempty"`
}

// Historical reports use the immutable attributed context, so editing today's
// candidate does not rewrite or erase a completed pass.
func (s *RecipeExecutions) characterBatchBasis(ctx context.Context, r RecipeExecution) (characterBatchRoot, novel.CharacterDesign, error) {
	var root characterBatchRoot
	var empty novel.CharacterDesign
	if json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != characterBatchKind {
		return root, empty, domain.ErrValidation
	}
	if err := validateCharacterBatchSteps(r, root); err != nil {
		return root, empty, err
	}
	var raw []byte
	var hash string
	err := s.Generations.IP.Workspace.DB.QueryRowContext(ctx, `SELECT payload,content_hash FROM context_packages WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?`, root.ContextID, r.ProjectID, r.BranchID, r.ActorID).Scan(&raw, &hash)
	if err != nil {
		return root, empty, err
	}
	var pack domain.ContextPackage
	if json.Unmarshal(raw, &pack) != nil || pack.ID != root.ContextID {
		return root, empty, domain.ErrMaterialExpired
	}
	canonical, err := json.Marshal(pack)
	if err != nil || digest(string(canonical)) != hash {
		return root, empty, domain.ErrNotReady
	}
	for _, item := range pack.Unconfirmed {
		if item.Ref.Kind != "resource_draft" || item.Ref.ID != root.ResourceID || item.Ref.Version != strconv.FormatInt(root.Request.Revision, 10) {
			continue
		}
		for _, part := range item.Parts {
			var workspace characterWorkspace
			if json.Unmarshal([]byte(part.Text), &workspace) != nil || workspace.Schema != "novel-character-workspace-v1" {
				continue
			}
			if workspace.ActorID != r.ActorID || workspace.Design.BookID != root.BookID || capabilityDigest(workspace.Design) != root.Request.Hash {
				return root, empty, domain.ErrConflict
			}
			return root, workspace.Design, novel.ValidateCharacterDesign(workspace.Design)
		}
	}
	return root, empty, domain.ErrNotReady
}

// override is the current child's output before committing its COMPLETED state.
// Its invalid identity/lock/relation results must not feed a subsequent pass.
func (s *RecipeExecutions) foldCharacterBatch(ctx context.Context, r RecipeExecution, through int, overrideID string, override []byte) (novel.CharacterDesign, []CharacterBatchPass, error) {
	root, d, err := s.characterBatchBasis(ctx, r)
	if err != nil {
		return d, nil, err
	}
	passes := []CharacterBatchPass{}
	for _, step := range r.Steps {
		if !step.Active || step.Ordinal > through {
			continue
		}
		if step.Ordinal != len(passes) {
			return d, passes, domain.ErrNotReady
		}
		raw := override
		if step.ID != overrideID {
			if step.State != "COMPLETED" {
				break
			}
			raw, err = s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, step.OutputHash)
			if err != nil || digest(string(raw)) != step.OutputHash {
				return d, passes, domain.ErrNotReady
			}
		}
		var input characterBatchStep
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil {
			return d, passes, domain.ErrNotReady
		}
		next, p, e := novel.ApplyCharacterChunk(d, input.IDs, raw, root.ResourceID, step.GenerationID)
		if e != nil {
			return d, passes, fmt.Errorf("%w：本段人物数量、身份、锁定设定或关系不符合要求", domain.ErrValidation)
		}
		d = next
		passes = append(passes, CharacterBatchPass{Key: step.Key, IDs: input.IDs, Result: p})
	}
	if overrideID != "" && len(passes) != through+1 {
		return d, passes, domain.ErrNotReady
	}
	return d, passes, nil
}

func (s *RecipeExecutions) validateCharacterBatchOutput(ctx context.Context, r RecipeExecution, step RecipeStepRun, raw []byte) error {
	var root characterBatchRoot
	_ = json.Unmarshal(r.Frozen.Input, &root)
	if root.Kind != characterBatchKind {
		return nil
	}
	_, _, err := s.foldCharacterBatch(ctx, r, step.Ordinal, step.ID, raw)
	return err
}

func (s *NovelCharacters) BatchList(ctx context.Context, actor, bookID, id string) ([]RecipeExecution, error) {
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, _, _, err = s.readTx(ctx, tx, a, book, id); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+recipeColumns+` FROM recipe_runs WHERE project_id=? AND branch_id=? AND actor_id=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.input.kind'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.input.book_id'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.input.resource_id'))=? ORDER BY created_at DESC,id LIMIT 100`, book.ProjectID, book.BranchID, actor, characterBatchKind, bookID, id)
	if err != nil {
		return nil, err
	}
	out := []RecipeExecution{}
	for rows.Next() {
		r, e := scanRecipe(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err = recipeFeesTx(ctx, tx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *NovelCharacters) BatchReport(ctx context.Context, actor, bookID, id, runID string) (CharacterBatchReport, error) {
	out := CharacterBatchReport{Passes: []CharacterBatchPass{}}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	tx.Rollback()
	recipes := &RecipeExecutions{Generations: s.Generations}
	r, err := recipes.Read(ctx, a, book.ProjectID, runID)
	if err != nil {
		return out, err
	}
	var root characterBatchRoot
	if r.ActorID != actor || r.BranchID != book.BranchID || json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != characterBatchKind || root.BookID != bookID || root.ResourceID != id {
		return out, domain.ErrForbidden
	}
	out.Run = r
	out.Total = len(root.IDs)
	d, passes, err := recipes.foldCharacterBatch(ctx, r, 31, "", nil)
	if err != nil {
		return out, err
	}
	out.Passes = passes
	for _, pass := range passes {
		out.Completed += len(pass.IDs)
	}
	tx, a, book, err = s.begin(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	current, _, _, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return out, err
	}
	err = recipes.validateCharacterBatchTx(ctx, tx, r)
	out.Stale = err != nil
	if out.Stale {
		out.StaleReason = "候选、小说设置或角色库已修改；历史结果仍可查看，请按当前设定重新准备。"
	}
	if !out.Stale && r.State == "COMPLETED" && !r.CancelRequested && out.Completed == out.Total {
		d.GenerationID = ""
		d.RecipeRunID = r.ID
		current.Document = d
		warnings := []string{}
		for _, pass := range passes {
			warnings = append(warnings, pass.Result.Warnings...)
		}
		out.Suggestion = &CharacterDesignSuggestion{Draft: current, Explanation: fmt.Sprintf("%d段已完整生成%d位角色；请检查人物差异和关系，确认后保存候选。", len(passes), out.Completed), Warnings: warnings}
	}
	return out, nil
}
