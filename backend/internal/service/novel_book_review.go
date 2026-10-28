package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

const novelReviewKind = "novel-book-review-v1"
const novelReviewWindowBytes = 72 * 1024

type NovelBookReviews struct {
	Generations *Generations
	Knowledge   *Knowledge
}

type NovelBookReviewInput struct {
	Source            string `json:"source"`
	VolumeID          string `json:"volume_id"`
	PreviewHash       string `json:"preview_hash"`
	AllowIncomplete   bool   `json:"allow_incomplete"`
	Notes             string `json:"notes"`
	AgentVersionID    string `json:"agent_version_id"`
	PriceID           string `json:"price_id"`
	Currency          string `json:"currency"`
	BudgetLimitMicros int64  `json:"budget_limit_micros"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
	OperationID       string `json:"operation_id"`
}

type novelReviewRoot struct {
	Kind    string               `json:"kind"`
	BookID  string               `json:"book_id"`
	Request NovelBookReviewInput `json:"request"`
	Preview NovelExportPreview   `json:"preview"`
}

type novelReviewInput struct {
	BookID     string          `json:"book_id"`
	ChapterIDs []string        `json:"chapter_ids"`
	Notes      string          `json:"notes"`
	Previous   json.RawMessage `json:"previous"`
}

func reviewExportInput(in NovelBookReviewInput) NovelExportInput {
	return NovelExportInput{Source: in.Source, VolumeID: in.VolumeID, Author: "审稿", Language: "zh-Hans"}
}

func reviewWindows(p NovelExportPreview, manuscript novel.ExportBook) ([][]NovelExportChapter, error) {
	windows := [][]NovelExportChapter{}
	current := []NovelExportChapter{}
	bytes, index := 0, 0
	for _, chapter := range p.Chapters {
		if !chapter.Included {
			continue
		}
		if index >= len(manuscript.Chapters) {
			return nil, domain.ErrNotReady
		}
		size := len(manuscript.Chapters[index].Text)
		index++
		if size > novelReviewWindowBytes {
			return nil, fmt.Errorf("%w：章节《%s》超过单次审稿全文上限，请先拆分章节", domain.ErrValidation, chapter.Title)
		}
		if len(current) > 0 && (len(current) == 8 || bytes+size > novelReviewWindowBytes || current[0].VolumeID != chapter.VolumeID) {
			windows = append(windows, current)
			current = []NovelExportChapter{}
			bytes = 0
		}
		current = append(current, chapter)
		bytes += size
	}
	if len(current) > 0 {
		windows = append(windows, current)
	}
	if len(windows) == 0 || len(windows) > 32 {
		return nil, fmt.Errorf("%w：请提供已保存正文；超过32段时请选择分卷审稿", domain.ErrValidation)
	}
	return windows, nil
}

type NovelBookReviewPreview struct {
	NovelExportPreview
	Passes int `json:"passes"`
}

func (s *NovelBookReviews) Preview(ctx context.Context, actor, bookID string, in NovelBookReviewInput) (NovelBookReviewPreview, error) {
	p, manuscript, err := (&NovelExport{IP: s.Generations.IP}).snapshot(ctx, actor, bookID, reviewExportInput(in))
	out := NovelBookReviewPreview{NovelExportPreview: p}
	if err != nil {
		return out, err
	}
	windows, err := reviewWindows(p, manuscript)
	out.Passes = len(windows)
	return out, err
}

func (s *NovelBookReviews) Prepare(ctx context.Context, actor, bookID string, in NovelBookReviewInput) (RecipeExecution, error) {
	var empty RecipeExecution
	if !validName(in.OperationID, 90) || len([]rune(in.Notes)) > 6000 || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 || in.BudgetLimitMicros < 1 {
		return empty, domain.ErrValidation
	}
	ip := s.Generations.IP
	tx, a, book, err := (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, true)
	if err != nil {
		return empty, err
	}
	prior, _, receiptErr := requestReceiptTx(ctx, tx, a, "recipe_runs", in.OperationID, false)
	tx.Rollback()
	recipes := &RecipeExecutions{Generations: s.Generations}
	if receiptErr == nil {
		r, e := recipes.Read(ctx, a, book.ProjectID, prior)
		if e != nil {
			return empty, e
		}
		var root novelReviewRoot
		if json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != novelReviewKind || root.BookID != bookID || capabilityDigest(root.Request) != capabilityDigest(in) {
			return empty, domain.ErrConflict
		}
		return r, nil
	}
	if receiptErr != sql.ErrNoRows {
		return empty, receiptErr
	}
	p, manuscript, err := (&NovelExport{IP: ip}).snapshot(ctx, actor, bookID, reviewExportInput(in))
	if err != nil {
		return empty, err
	}
	if in.PreviewHash == "" || in.PreviewHash != p.Hash {
		return empty, domain.ErrConflict
	}
	if (p.Missing > 0 || p.Warnings > 0) && !in.AllowIncomplete {
		return empty, fmt.Errorf("%w：请明确确认缺章或待更新的写作依据", domain.ErrValidation)
	}
	windows, err := reviewWindows(p, manuscript)
	if err != nil {
		return empty, err
	}
	_, recipe, err := s.install(ctx, a, book.ProjectID, len(windows), in.AgentVersionID)
	if err != nil {
		return empty, err
	}
	if _, err = s.Knowledge.Status(ctx, a, book.ProjectID, book.BranchID); err != nil {
		if !errors.Is(err, domain.ErrNotReady) {
			return empty, err
		}
		if _, err = s.Knowledge.Build(ctx, a, book.ProjectID, book.BranchID); err != nil {
			return empty, err
		}
	}
	root := novelReviewRoot{Kind: novelReviewKind, BookID: bookID, Request: in, Preview: p}
	raw, _ := json.Marshal(root)
	request := RecipeExecutionInput{RecipeVersionID: recipe.ID, BranchID: book.BranchID, OperationID: in.OperationID, Trial: true, Input: raw, Currency: in.Currency, BudgetLimitMicros: in.BudgetLimitMicros, Steps: []RecipeStepInput{}}
	textByChapter := map[string]string{}
	textIndex := 0
	for _, chapter := range p.Chapters {
		if chapter.Included {
			textByChapter[chapter.ID] = manuscript.Chapters[textIndex].Text
			textIndex++
		}
	}
	for i, window := range windows {
		ctxInput := SearchInput{Purpose: "critic", Query: "审查人物发展、因果、伏笔、主题、主线高光及语言质量", StoryTime: 9007199254740991, Limit: 8, TokenBudget: 262144, IncludeWorking: true}
		// Bound only the retrieval query; full chapter sources remain mandatory.
		query := []rune(in.Notes + "\n" + textByChapter[window[0].ID])
		if len(query) > 900 {
			query = query[:900]
		}
		ctxInput.Query += "\n" + string(query)
		// Planning sources are whole immutable resources, never RAG snippets.
		tx, _, _, err = (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, true)
		if err != nil {
			return empty, err
		}
		check, e := newPlanningCheck(ctx, tx, ip, book.ProjectID, book.BranchID)
		if e == nil {
			for _, key := range [][2]string{{"world", ""}, {"book", ""}, {"volume", window[0].VolumeID}} {
				var resource domain.Resource
				resource, _, e = check.current(key[0], key[1])
				if e != nil {
					break
				}
				ctxInput.RequiredResourceIDs = append(ctxInput.RequiredResourceIDs, resource.ID)
			}
		}
		tx.Rollback()
		if e != nil {
			return empty, e
		}
		stepInput := novelReviewInput{BookID: bookID, ChapterIDs: []string{}, Notes: in.Notes, Previous: json.RawMessage(`{}`)}
		for _, chapter := range window {
			stepInput.ChapterIDs = append(stepInput.ChapterIDs, chapter.ID)
			if in.Source == "draft" {
				// Export revision may refer to a published fallback; check private
				// storage so a public revision is never treated as a draft version.
				tx, _, _, e = (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, true)
				if e != nil {
					return empty, e
				}
				d, readErr := resourceDraftTx(ctx, tx, a.ID, book.ProjectID, chapter.ResourceID)
				tx.Rollback()
				if readErr != nil {
					return empty, readErr
				}
				if d.Revision > 0 {
					ctxInput.WorkingRefs = append(ctxInput.WorkingRefs, domain.WorkingRef{Kind: "resource_draft", ID: chapter.ResourceID, Version: strconv.FormatInt(d.Revision, 10)})
					continue
				}
			}
			ctxInput.RequiredResourceIDs = append(ctxInput.RequiredResourceIDs, chapter.ResourceID)
		}
		pack, e := s.Knowledge.Search(ctx, a, book.ProjectID, book.BranchID, ctxInput)
		if e != nil {
			return empty, e
		}
		if i > 0 && request.ExpectedIPHead != pack.IPHead {
			return empty, domain.ErrConflict
		}
		request.ExpectedIPHead = pack.IPHead
		payload, _ := json.Marshal(stepInput)
		step := RecipeStepInput{Key: fmt.Sprintf("review-%d", i+1), PriceID: in.PriceID, MaxOutputTokens: in.MaxOutputTokens, ContextID: pack.ID, Input: payload, Bindings: []domain.RecipeValueBinding{}}
		if i > 0 {
			step.Bindings = append(step.Bindings, domain.RecipeValueBinding{TargetPointer: "/previous", SourceStep: fmt.Sprintf("review-%d", i), SourcePointer: ""})
		}
		request.Steps = append(request.Steps, step)
	}
	return recipes.Prepare(ctx, a, book.ProjectID, request)
}

// Runs are bound to the complete author-visible snapshot, including missing
// chapters. Saving any selected chapter or changing the directory invalidates it.
func (s *RecipeExecutions) validateNovelReviewTx(ctx context.Context, tx *sql.Tx, r RecipeExecution) error {
	var root novelReviewRoot
	_ = json.Unmarshal(r.Frozen.Input, &root)
	if root.Kind != novelReviewKind {
		for _, step := range r.Steps {
			if step.Frozen.Agent.Definition.Key == "novel-book-review" {
				return domain.ErrValidation
			}
		}
		return nil
	}
	if err := validateNovelReviewSteps(r, root); err != nil {
		return err
	}
	if root.Request.Source != "draft" && root.Request.Source != "published" {
		return domain.ErrValidation
	}
	a := domain.Actor{ID: r.ActorID, WorkspaceID: r.WorkspaceID}
	book, err := readNovelTx(ctx, tx, a, root.BookID)
	if err != nil {
		return err
	}
	if book.ProjectID != r.ProjectID || book.BranchID != r.BranchID {
		return domain.ErrForbidden
	}
	p, _, err := (&NovelExport{IP: s.Generations.IP}).snapshotTx(ctx, tx, a, book, reviewExportInput(root.Request))
	if err != nil {
		return err
	}
	if p.Hash != root.Preview.Hash || p.Hash != root.Request.PreviewHash || capabilityDigest(p) != capabilityDigest(root.Preview) {
		return domain.ErrConflict
	}
	if strings.TrimSpace(root.BookID) == "" {
		return domain.ErrValidation
	}
	return nil
}
