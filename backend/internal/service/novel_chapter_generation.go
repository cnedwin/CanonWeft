package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/presets"
)

type NovelChapterWriter struct {
	Generations *Generations
	Knowledge   *Knowledge
}
type NovelChapterWritingInput struct {
	Method          string `json:"method,omitempty"`
	AgentVersionID  string `json:"agent_version_id,omitempty"`
	Revision        int64  `json:"revision"`
	Hash            string `json:"hash"`
	Notes           string `json:"notes"`
	PriceID         string `json:"price_id"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	OperationID     string `json:"operation_id"`
}

func (s *IP) validateNovelChapterSourceTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	ref := g.Frozen.NovelChapter
	if ref == nil {
		return nil
	}
	if (g.Frozen.Role != "writer" && g.Frozen.Role != "bookkeeper") || (g.Frozen.Role == "writer" && g.Frozen.NovelDetail == nil) || (g.Frozen.Role == "bookkeeper" && g.Frozen.NovelDetail != nil) {
		return domain.ErrValidation
	}
	var book NovelBook
	if err := tx.QueryRowContext(ctx, "SELECT id,project_id,branch_id FROM novels WHERE id=?", ref.BookID).Scan(&book.ID, &book.ProjectID, &book.BranchID); err != nil {
		return err
	}
	if book.ProjectID != g.ProjectID || book.BranchID != g.BranchID {
		return domain.ErrForbidden
	}
	d, err := (&NovelChapters{IP: s}).draftTx(ctx, tx, domain.Actor{ID: g.ActorID}, book, ref.ResourceID)
	if err != nil {
		return err
	}
	if d.Revision != ref.Revision || d.Hash != ref.Hash || d.BaseHead != ref.BaseHead || !d.Ready || (g.Frozen.NovelDetail != nil && d.Document.Detail != *g.Frozen.NovelDetail) {
		return fmt.Errorf("%w：章稿或写作依据已变化，请重新准备本章请求", domain.ErrConflict)
	}
	return nil
}

func (s *NovelChapterWriter) Prepare(ctx context.Context, actor, bookID, id string, in NovelChapterWritingInput) (Generation, error) {
	return s.prepareRole(ctx, actor, bookID, id, "writer", in)
}

func (s *NovelChapterWriter) PrepareBookkeeper(ctx context.Context, actor, bookID, id string, in NovelChapterWritingInput) (Generation, error) {
	return s.prepareRole(ctx, actor, bookID, id, "bookkeeper", in)
}

func (s *NovelChapterWriter) prepareRole(ctx context.Context, actor, bookID, id, role string, in NovelChapterWritingInput, capture ...func(domain.Actor, string, GenerationInput) (Generation, error)) (Generation, error) {
	if in.Method != "" && (in.Method != "fanqie-polish" || role != "writer") {
		return Generation{}, domain.ErrValidation
	}
	if !validName(strings.TrimSpace(in.Notes), 10000) || !validName(in.OperationID, 120) || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 {
		return Generation{}, domain.ErrValidation
	}
	ip := s.Generations.IP
	chapters := &NovelChapters{IP: ip}
	tx, a, book, err := (&NovelPlanning{IP: ip}).begin(ctx, actor, bookID, true)
	if err != nil {
		return Generation{}, err
	}
	defer tx.Rollback()
	fingerprint := capabilityDigest([]any{"novel-chapter-" + role + "-v1", bookID, id, in})
	previous, hash, e := requestReceiptTx(ctx, tx, a, "generation_tasks", in.OperationID, false)
	if e == nil {
		if hash != fingerprint {
			return Generation{}, domain.ErrConflict
		}
		return generationTx(ctx, tx, book.ProjectID, previous)
	}
	if e != sql.ErrNoRows {
		return Generation{}, e
	}
	draft, err := chapters.draftTx(ctx, tx, a, book, id)
	if err != nil {
		return Generation{}, err
	}
	if !draft.Ready || draft.Revision < 1 || draft.Revision != in.Revision || draft.Hash != in.Hash {
		return Generation{}, fmt.Errorf("%w：%s", domain.ErrConflict, draft.Reason)
	}
	if role == "bookkeeper" && strings.TrimSpace(draft.Document.Text) == "" {
		return Generation{}, fmt.Errorf("%w：请先写入本章正文，再提取剧情账本", domain.ErrValidation)
	}
	if in.Method == "fanqie-polish" && strings.TrimSpace(draft.Document.Text) == "" {
		return Generation{}, fmt.Errorf("%w：请先写入需要润稿的正文", domain.ErrValidation)
	}
	stored, err := resourceDraftTx(ctx, tx, actor, book.ProjectID, id)
	if err != nil {
		return Generation{}, err
	}
	input := domain.NovelChapterRequest{Draft: json.RawMessage(stored.Text), Plans: []domain.RequiredInput{}, Notes: in.Notes, Style: book.Setup.Style, Language: book.Setup.Language, Narration: book.Setup.Perspective, ChapterMin: book.Setup.ChapterMin, ChapterMax: book.Setup.ChapterMax}
	input.Draft, err = ip.chapterResearchDraftTx(ctx, tx, book, draft.Document, stored.Text)
	if err != nil {
		return Generation{}, err
	}
	check, err := newPlanningCheck(ctx, tx, ip, book.ProjectID, book.BranchID)
	if err != nil {
		return Generation{}, err
	}
	var writingCard *NovelWritingCard
	if role == "writer" {
		input.Plans, err = chapterWritingInputs(check, draft.Document.ChapterID)
		if err != nil {
			return Generation{}, err
		}
		card, e := buildNovelWritingCard(book.Setup, draft.Document, input.Plans)
		if e != nil {
			return Generation{}, e
		}
		writingCard = &card
		input.Draft, err = attachNovelWritingCard(input.Draft, card)
		if err != nil {
			return Generation{}, err
		}
	}
	tx.Rollback()
	query := []rune(in.Notes)
	if writingCard != nil {
		query = []rune(novelWritingCardQuery(*writingCard, in.Notes))
	}
	if len(query) > 1000 {
		query = query[:1000]
	}
	pack, err := s.Knowledge.Search(ctx, a, book.ProjectID, book.BranchID, SearchInput{Purpose: contextPurposeForRole(role), Query: string(query), StoryTime: draft.Document.StoryTime, Perspective: draft.Document.Perspective, Limit: 12, TokenBudget: 131072})
	if err != nil {
		return Generation{}, fmt.Errorf("本章资料检索：%w", err)
	}
	bundleName := "novel-chapter-" + role
	if in.Method == "fanqie-polish" {
		bundleName = "fanqie-editorial-polish"
	}
	bundle, err := presets.Bundle(bundleName)
	if err != nil {
		return Generation{}, fmt.Errorf("本章写作预置方法：%w", err)
	}
	raw, _ := json.Marshal(input)
	if len(raw) > 512*1024 {
		return Generation{}, fmt.Errorf("%w：本章必要规划与章稿超过容量，请缩小本卷规划后重试", domain.ErrContextBudget)
	}
	detailID := ""
	if role == "writer" {
		detailID = draft.Document.Detail.ID
	}
	generation := GenerationInput{novelPlanningFingerprint: fingerprint, novelChapter: &domain.NovelChapterSource{BookID: bookID, ResourceID: id, Revision: draft.Revision, Hash: draft.Hash, BaseHead: draft.BaseHead}, NovelDetailID: detailID, BranchID: book.BranchID, ExpectedIPHead: pack.IPHead, Role: role, ContextID: pack.ID, PriceID: in.PriceID, OperationID: in.OperationID, Request: models.Request{System: string(bundle.Find("SKILL.md")), Prompt: string(raw), Schema: bundle.Find("output.schema.json"), MaxOutputTokens: in.MaxOutputTokens}}
	if in.AgentVersionID != "" {
		generation.AgentVersionID = in.AgentVersionID
		generation.AgentInput = raw
		generation.CapabilityTrial = true
		generation.Request.System = "根据服务器固定的本章输入和作者要求提出候选。不得修改身份、来源版本、权限或确认状态；所选方法为明确试用，不代表质量资格。"
		generation.Request.Prompt = "请使用所选方法处理本章输入，返回符合输出契约的候选。"
		generation.Request.Schema = nil
	}
	if writingCard != nil {
		generation.Request.System += "\nwriting_card是从当前有效规划派生的本章写作摘要，完整规划仍在plans。recent_plan是近期规划，不代表事件已发生；真实承接使用知识包中经过分支、时间和视角过滤的已采用记忆。题材指导服从作者当前选择，不能改写主线、人物知情边界或把推测升格为事实。比较近期章节的冲突与局面变化，避免机械重复，不为求异强加反转。"
	}
	if len(capture) == 1 {
		generation.Request.System += "\n" + chapterResearchInstructions
		return capture[0](a, book.ProjectID, generation)
	}
	generation.Request.System += "\n" + chapterResearchInstructions
	return s.Generations.Prepare(ctx, a, book.ProjectID, generation)
}

func (s *NovelChapterWriter) Suggestion(ctx context.Context, actor, bookID, id, task string) (NovelChapterDraft, error) {
	chapters := &NovelChapters{IP: s.Generations.IP}
	tx, a, book, err := (&NovelPlanning{IP: chapters.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelChapterDraft{}, err
	}
	tx.Rollback()
	g, err := s.Generations.Read(ctx, a, book.ProjectID, task)
	if err != nil {
		return NovelChapterDraft{}, err
	}
	if g.ActorID != actor || g.BranchID != book.BranchID || g.State != "SUCCEEDED" || g.Cancelled || g.Output == nil || g.Frozen.NovelChapter == nil || g.Frozen.NovelChapter.ResourceID != id || g.Frozen.NovelChapter.BookID != bookID {
		return NovelChapterDraft{}, domain.ErrConflict
	}
	tx, a, book, err = (&NovelPlanning{IP: chapters.IP}).begin(ctx, actor, bookID, true)
	if err != nil {
		return NovelChapterDraft{}, err
	}
	defer tx.Rollback()
	if err = chapters.IP.validateNovelChapterSourceTx(ctx, tx, g); err != nil {
		return NovelChapterDraft{}, err
	}
	if g.Frozen.RecipeRunID != "" {
		if err = s.Generations.validateRecipeChildTx(ctx, tx, g, true); err != nil {
			return NovelChapterDraft{}, err
		}
	}
	draft, err := chapters.draftTx(ctx, tx, a, book, id)
	if err != nil {
		return draft, err
	}
	output, err := novel.ChapterOutput([]byte(g.Output.Text))
	if err != nil {
		return draft, err
	}
	if output.Title != "" {
		draft.Document.Title = output.Title
	}
	draft.Document.Text = output.Text
	draft.Notes = output.Notes
	draft.Document.WriterGenerationID = g.ID
	return draft, nil
}
