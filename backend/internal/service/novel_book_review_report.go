package service

import (
	"context"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"slices"
)

type NovelBookReviewPass struct {
	Key        string                 `json:"key"`
	ChapterIDs []string               `json:"chapter_ids"`
	Result     novel.BookReviewResult `json:"result"`
}
type NovelBookReviewReport struct {
	Run         RecipeExecution       `json:"run"`
	Preview     NovelExportPreview    `json:"preview"`
	Stale       bool                  `json:"stale"`
	StaleReason string                `json:"stale_reason"`
	Passes      []NovelBookReviewPass `json:"passes"`
	Covered     int                   `json:"covered"`
	Limits      []string              `json:"limits"`
}

// Only immutable context payloads are used for historical evidence. Re-reading
// today's draft would erase old reports or accidentally validate a new quote.
func (s *RecipeExecutions) reviewCorpus(ctx context.Context, r RecipeExecution, through int) (map[string]string, error) {
	var root novelReviewRoot
	if json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != novelReviewKind {
		return nil, domain.ErrValidation
	}
	pins := map[string]string{}
	for _, c := range root.Preview.Chapters {
		if c.Included {
			pins[c.ID] = c.Hash
		}
	}
	corpus := map[string]string{}
	for _, step := range r.Steps {
		if !step.Active || step.Ordinal > through {
			continue
		}
		var input novelReviewInput
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil {
			return nil, domain.ErrValidation
		}
		var raw []byte
		var hash string
		err := s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT payload,content_hash FROM context_packages WHERE id=? AND project_id=? AND branch_id=? AND actor_id=?", step.Frozen.Input.ContextID, r.ProjectID, r.BranchID, r.ActorID).Scan(&raw, &hash)
		if err != nil {
			return nil, err
		}
		var pack domain.ContextPackage
		if json.Unmarshal(raw, &pack) != nil || pack.ID != step.Frozen.Input.ContextID {
			return nil, domain.ErrMaterialExpired
		}
		canonical, err := json.Marshal(pack)
		if err != nil || digest(string(canonical)) != hash {
			return nil, domain.ErrNotReady
		}
		add := func(text string) {
			doc, e := novel.DecodeChapter([]byte(text))
			if e == nil && slices.Contains(input.ChapterIDs, doc.ChapterID) && digest(text) == pins[doc.ChapterID] {
				corpus[doc.ChapterID] = doc.Text
			}
		}
		for _, part := range pack.Required {
			add(part.Text)
		}
		for _, working := range pack.Unconfirmed {
			for _, part := range working.Parts {
				add(part.Text)
			}
		}
		for _, id := range input.ChapterIDs {
			if _, ok := corpus[id]; !ok {
				return nil, fmt.Errorf("%w：缺少固定版本章节全文", domain.ErrNotReady)
			}
		}
	}
	return corpus, nil
}

func (s *RecipeExecutions) validateNovelReviewOutput(ctx context.Context, r RecipeExecution, step RecipeStepRun, raw []byte) error {
	var root novelReviewRoot
	_ = json.Unmarshal(r.Frozen.Input, &root)
	if root.Kind != novelReviewKind {
		return nil
	}
	var input novelReviewInput
	if json.Unmarshal(step.Frozen.Input.Input, &input) != nil {
		return domain.ErrValidation
	}
	corpus, err := s.reviewCorpus(ctx, r, step.Ordinal)
	if err != nil {
		return err
	}
	_, err = novel.ReadBookReview(raw, input.ChapterIDs, corpus)
	return err
}

func (s *NovelBookReviews) List(ctx context.Context, actor, bookID string) ([]RecipeExecution, error) {
	tx, a, book, err := (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Filter before limiting so unrelated writing/research runs cannot hide the
	// author's saved review tasks from the history panel.
	rows, err := tx.QueryContext(ctx, "SELECT "+recipeColumns+" FROM recipe_runs WHERE project_id=? AND branch_id=? AND actor_id=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.input.kind'))=? AND JSON_UNQUOTE(JSON_EXTRACT(frozen,'$.input.book_id'))=? ORDER BY created_at DESC,id LIMIT 100", book.ProjectID, book.BranchID, a.ID, novelReviewKind, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecipeExecution{}
	for rows.Next() {
		r, e := scanRecipe(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range out {
		if err = recipeFeesTx(ctx, tx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *NovelBookReviews) Report(ctx context.Context, actor, bookID, id string) (NovelBookReviewReport, error) {
	out := NovelBookReviewReport{Passes: []NovelBookReviewPass{}, Limits: []string{}}
	tx, a, book, err := (&NovelPlanning{IP: s.Generations.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	tx.Rollback()
	recipes := &RecipeExecutions{Generations: s.Generations}
	r, err := recipes.Read(ctx, a, book.ProjectID, id)
	if err != nil {
		return out, err
	}
	var root novelReviewRoot
	if r.ActorID != a.ID || r.BranchID != book.BranchID || json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != novelReviewKind || root.BookID != bookID {
		return out, domain.ErrForbidden
	}
	out.Run = r
	out.Preview = root.Preview
	current, e := (&NovelExport{IP: s.Generations.IP}).Preview(ctx, actor, bookID, reviewExportInput(root.Request))
	out.Stale = e != nil || current.Hash != root.Preview.Hash
	if out.Stale {
		out.StaleReason = "目录、设置或章稿已改变，或当前依据已不可用；此报告只对应审稿时的固定版本。"
	}
	for _, step := range r.Steps {
		if !step.Active || step.State != "COMPLETED" {
			continue
		}
		var input novelReviewInput
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil {
			return out, domain.ErrNotReady
		}
		corpus, e := recipes.reviewCorpus(ctx, r, step.Ordinal)
		if e != nil {
			out.Limits = append(out.Limits, step.Key+"：历史正文来源已不可用，无法复核该段报告。")
			continue
		}
		result, e := novel.ReadBookReview(step.Output, input.ChapterIDs, corpus)
		if e != nil {
			return out, e
		}
		out.Passes = append(out.Passes, NovelBookReviewPass{Key: step.Key, ChapterIDs: input.ChapterIDs, Result: result})
		out.Covered += len(result.Coverage)
	}
	return out, nil
}

// The generic recipe API must enforce the same coverage contract as the novel
// entry point; callers cannot remove windows or bind over chapter identity.
func validateNovelReviewSteps(r RecipeExecution, root novelReviewRoot) error {
	seen := []string{}
	active := 0
	for _, step := range r.Steps {
		if !step.Active {
			continue
		}
		active++
		var input novelReviewInput
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil || input.BookID != root.BookID || len(input.ChapterIDs) < 1 || len(input.ChapterIDs) > 8 || step.Frozen.Agent.Definition.Role != "critic" || step.Frozen.Input.ContextID == "" {
			return domain.ErrValidation
		}
		if step.Ordinal == 0 && len(step.Frozen.Input.Bindings) != 0 {
			return domain.ErrValidation
		}
		if step.Ordinal > 0 {
			b := step.Frozen.Input.Bindings
			if len(b) != 1 || b[0].TargetPointer != "/previous" || b[0].SourceStep != fmt.Sprintf("review-%d", step.Ordinal) || b[0].SourcePointer != "" {
				return domain.ErrValidation
			}
		}
		if step.Key != fmt.Sprintf("review-%d", step.Ordinal+1) {
			return domain.ErrValidation
		}
		seen = append(seen, input.ChapterIDs...)
	}
	expected := []string{}
	for _, c := range root.Preview.Chapters {
		if c.Included {
			expected = append(expected, c.ID)
		}
	}
	if active < 1 || active > 32 || !slices.Equal(seen, expected) {
		return domain.ErrValidation
	}
	return nil
}
