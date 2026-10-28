package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

type NovelBatch struct{ Writer *NovelChapterWriter }
type NovelBatchInput struct {
	BookID            string   `json:"book_id,omitempty"`
	ChapterIDs        []string `json:"chapter_ids"`
	Notes             string   `json:"notes"`
	Platform          string   `json:"platform"`
	Audience          string   `json:"audience"`
	Focus             string   `json:"focus"`
	PriceID           string   `json:"price_id"`
	Currency          string   `json:"currency"`
	BudgetLimitMicros int64    `json:"budget_limit_micros"`
	MaxOutputTokens   int      `json:"max_output_tokens"`
	OperationID       string   `json:"operation_id"`
}
type NovelBatchChapter struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Ready  bool   `json:"ready"`
	Reason string `json:"reason"`
}

func validateNovelBatchOrder(order, selected []string) error {
	if len(selected) < 1 || len(selected) > 10 {
		return domain.ErrValidation
	}
	start := slices.Index(order, selected[0])
	if start < 0 || start+len(selected) > len(order) {
		return domain.ErrValidation
	}
	for i, id := range selected {
		if id != order[start+i] {
			return fmt.Errorf("%w：请选择规划中连续且不重复的1—10章", domain.ErrValidation)
		}
	}
	return nil
}

func (s *NovelBatch) Chapters(ctx context.Context, actor, bookID string) ([]NovelBatchChapter, error) {
	ip := s.Writer.Generations.IP
	tx, _, book, err := (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	check, err := newPlanningCheck(ctx, tx, ip, book.ProjectID, book.BranchID)
	if err != nil {
		return nil, err
	}
	_, outline, err := check.current("book", "")
	if err != nil {
		return nil, err
	}
	items := []NovelBatchChapter{}
	for _, v := range outline.Volumes {
		_, volume, e := check.current("volume", v.ID)
		if e != nil {
			return nil, e
		}
		for _, chapter := range volume.Chapters {
			_, _, e := check.current("detail", chapter.ID)
			item := NovelBatchChapter{ID: chapter.ID, Title: chapter.Title, Ready: e == nil}
			if e != nil {
				item.Reason = "请先确认有效细纲"
			}
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *NovelBatch) Prepare(ctx context.Context, actor, bookID string, in NovelBatchInput) (RecipeExecution, error) {
	var out RecipeExecution
	if in.BookID != "" && in.BookID != bookID {
		return out, domain.ErrForbidden
	}
	in.BookID = bookID
	if !validName(in.OperationID, 90) || len([]rune(in.Notes)) > 6000 || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 || in.BudgetLimitMicros < 1 {
		return out, domain.ErrValidation
	}
	guidance, err := novel.EditorialGuidance(in.Platform, in.Audience, in.Focus)
	if err != nil {
		return out, err
	}
	ip := s.Writer.Generations.IP
	tx, a, book, err := (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, true)
	if err != nil {
		return out, err
	}
	// Resolve lost prepare replies before opening drafts or recomputing contexts.
	prior, _, priorErr := requestReceiptTx(ctx, tx, a, "recipe_runs", in.OperationID, false)
	tx.Rollback()
	if priorErr == nil {
		r, e := (&RecipeExecutions{Generations: s.Writer.Generations}).Read(ctx, a, book.ProjectID, prior)
		var frozen NovelBatchInput
		if e != nil {
			return r, e
		}
		if json.Unmarshal(r.Frozen.Input, &frozen) != nil || capabilityDigest(frozen) != capabilityDigest(in) {
			return out, domain.ErrConflict
		}
		return r, nil
	}
	if priorErr != sql.ErrNoRows {
		return out, priorErr
	}
	available, err := s.Chapters(ctx, actor, bookID)
	if err != nil {
		return out, err
	}
	order := []string{}
	for _, chapter := range available {
		order = append(order, chapter.ID)
		if slices.Contains(in.ChapterIDs, chapter.ID) && !chapter.Ready {
			return out, domain.ErrNotReady
		}
	}
	if err = validateNovelBatchOrder(order, in.ChapterIDs); err != nil {
		return out, err
	}
	agent, recipe, err := s.install(ctx, a, book.ProjectID, len(in.ChapterIDs))
	if err != nil {
		return out, err
	}
	raw, _ := json.Marshal(in)
	request := RecipeExecutionInput{RecipeVersionID: recipe.ID, BranchID: book.BranchID, OperationID: in.OperationID, Trial: true, Input: raw, Currency: in.Currency, BudgetLimitMicros: in.BudgetLimitMicros, Steps: []RecipeStepInput{}}
	chapters := &NovelChapters{IP: ip}
	for i, chapterID := range in.ChapterIDs {
		resource, e := chapters.Open(ctx, actor, bookID, chapterID)
		if e != nil {
			return out, e
		}
		draft, e := chapters.Read(ctx, actor, bookID, resource.ID)
		if e != nil {
			return out, e
		}
		if draft.Revision == 0 {
			draft, e = chapters.Save(ctx, actor, bookID, resource.ID, draft)
			if e != nil {
				return out, e
			}
		}
		input := NovelChapterWritingInput{Revision: draft.Revision, Hash: draft.Hash, Notes: guidance + "\n作者要求：" + strings.TrimSpace(in.Notes), PriceID: in.PriceID, MaxOutputTokens: in.MaxOutputTokens, OperationID: fmt.Sprintf("%s-ch-%d", in.OperationID, i+1)}
		_, e = s.Writer.prepareRole(ctx, actor, bookID, resource.ID, "writer", input, func(_ domain.Actor, _ string, g GenerationInput) (Generation, error) {
			var payload domain.NovelChapterRequest
			if json.Unmarshal([]byte(g.Request.Prompt), &payload) != nil {
				return Generation{}, domain.ErrValidation
			}
			payload.Previous = json.RawMessage(`{}`)
			encoded, _ := json.Marshal(payload)
			step := RecipeStepInput{Key: fmt.Sprintf("chapter-%d", i+1), PriceID: in.PriceID, MaxOutputTokens: in.MaxOutputTokens, ContextID: g.ContextID, Input: encoded, Bindings: []domain.RecipeValueBinding{}, NovelChapter: g.novelChapter, NovelDetailID: g.NovelDetailID}
			if i > 0 {
				step.Bindings = append(step.Bindings, domain.RecipeValueBinding{TargetPointer: "/previous", SourceStep: fmt.Sprintf("chapter-%d", i), SourcePointer: ""})
			}
			if i > 0 && request.ExpectedIPHead != g.ExpectedIPHead {
				return Generation{}, domain.ErrConflict
			}
			request.ExpectedIPHead = g.ExpectedIPHead
			request.Steps = append(request.Steps, step)
			return Generation{}, nil
		})
		if e != nil {
			return out, e
		}
	}
	_ = agent // The immutable recipe pins the installed writer agent.
	return (&RecipeExecutions{Generations: s.Writer.Generations}).Prepare(ctx, a, book.ProjectID, request)
}

// A chapter recipe cannot route an earlier result over its identity or plans.
func (s *RecipeExecutions) validateNovelRecipeStepTx(ctx context.Context, tx *sql.Tx, r RecipeExecution, step RecipeStepRun) error {
	ref := step.Frozen.Input.NovelChapter
	if ref == nil {
		return nil
	}
	var input domain.NovelChapterRequest
	if json.Unmarshal(step.Frozen.Input.Input, &input) != nil || len(input.Plans) != 4 {
		return domain.ErrValidation
	}
	doc, original, materials, err := decodeChapterResearchDraft(input.Draft)
	if err != nil || digest(string(original)) != ref.Hash || doc.Detail.ID != step.Frozen.Input.NovelDetailID || step.Frozen.Agent.Definition.Role != "writer" {
		return domain.ErrValidation
	}
	book := NovelBook{ID: ref.BookID, ProjectID: r.ProjectID, BranchID: r.BranchID}
	expected, err := s.Generations.IP.chapterResearchInputsTx(ctx, tx, book, doc)
	if err != nil {
		return err
	}
	if len(expected) != len(materials) || (len(expected) > 0 && capabilityDigest(expected) != capabilityDigest(materials)) {
		return domain.ErrValidation
	}
	for _, binding := range step.Frozen.Input.Bindings {
		if binding.TargetPointer != "/previous" || binding.SourcePointer != "" || step.Ordinal < 1 || binding.SourceStep != fmt.Sprintf("chapter-%d", step.Ordinal) {
			return domain.ErrValidation
		}
	}
	if step.Ordinal == 0 && len(step.Frozen.Input.Bindings) != 0 || step.Ordinal > 0 && len(step.Frozen.Input.Bindings) != 1 {
		return domain.ErrValidation
	}
	g := Generation{ProjectID: r.ProjectID, BranchID: r.BranchID, ActorID: r.ActorID, Frozen: GenerationFrozen{Role: "writer", NovelChapter: ref, NovelDetail: &doc.Detail}}
	err = s.Generations.IP.validateNovelChapterSourceTx(ctx, tx, g)
	if err == nil {
		return nil
	}
	// Explicit adoption after the whole batch completed changes the private
	// revision. Accept only the exact generated text, not a later manual edit.
	if r.State != "COMPLETED" || step.OutputHash == "" || step.GenerationID == "" {
		return err
	}
	current, readErr := (&NovelChapters{IP: s.Generations.IP}).draftTx(ctx, tx, domain.Actor{ID: r.ActorID}, book, ref.ResourceID)
	if readErr != nil || !current.Ready || current.BaseHead != ref.BaseHead || current.Document.WriterGenerationID != step.GenerationID {
		return err
	}
	raw, readErr := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, step.OutputHash)
	if readErr != nil {
		return readErr
	}
	candidate, readErr := novel.ChapterOutput(raw)
	if readErr != nil || candidate.Text != current.Document.Text || candidate.Title != current.Document.Title {
		return err
	}
	return nil
}
