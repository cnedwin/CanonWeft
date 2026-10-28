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
	"novelstudio/internal/models"
	"novelstudio/internal/novel"
	"novelstudio/presets"
)

type NovelPlanningAssistance struct {
	Knowledge   *Knowledge
	Generations *Generations
}
type NovelPlanningAssistInput struct {
	AgentVersionID  string `json:"agent_version_id,omitempty"`
	Revision        int64  `json:"revision"`
	Hash            string `json:"hash"`
	Notes           string `json:"notes"`
	PriceID         string `json:"price_id"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	OperationID     string `json:"operation_id"`
}
type NovelPlanningSuggestion struct {
	Draft       NovelPlanDraft `json:"draft"`
	Explanation string         `json:"explanation"`
	Questions   []string       `json:"questions"`
}

// Full selected sources enter the existing attributed context, alongside the
// exact private draft. No independent prompt store or execution authority.
func (s *NovelPlanningAssistance) Prepare(ctx context.Context, actor, bookID, resource string, in NovelPlanningAssistInput) (Generation, error) {
	ip := s.Generations.IP
	plans := &NovelPlanning{IP: ip}
	if !validName(in.OperationID, 120) || !validName(strings.TrimSpace(in.Notes), 10000) || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 {
		return Generation{}, domain.ErrValidation
	}
	tx, a, book, err := plans.begin(ctx, actor, bookID, true)
	if err != nil {
		return Generation{}, err
	}
	defer tx.Rollback()
	fingerprint := capabilityDigest([]any{"novel-planning-v1", bookID, resource, in})
	previous, stored, replayErr := requestReceiptTx(ctx, tx, a, "generation_tasks", in.OperationID, false)
	if replayErr == nil {
		if stored != fingerprint {
			return Generation{}, domain.ErrConflict
		}
		return generationTx(ctx, tx, book.ProjectID, previous)
	}
	if replayErr != sql.ErrNoRows {
		return Generation{}, replayErr
	}
	draft, check, err := plans.draftTx(ctx, tx, a, book, resource)
	if err != nil {
		return Generation{}, err
	}
	if draft.Revision < 1 || draft.Revision != in.Revision || draft.Hash != in.Hash || draft.BaseHead != draft.Resource.Head {
		return Generation{}, domain.ErrConflict
	}
	if err = check.validateSettings(draft.Document); err != nil {
		return Generation{}, err
	}
	required := []string{}
	seen := map[string]bool{}
	add := func(r domain.Resource) {
		if !seen[r.ID] {
			seen[r.ID] = true
			required = append(required, r.ID)
		}
	}
	if draft.Document.Kind != "world" {
		world, _, e := check.current("world", "")
		if e != nil {
			return Generation{}, e
		}
		add(world)
	}
	if draft.Document.Kind == "detail" {
		outline, doc, e := check.current("book", "")
		if e != nil {
			return Generation{}, e
		}
		add(outline)
		found := false
		for _, volume := range doc.Volumes {
			r, v, e := check.current("volume", volume.ID)
			if e != nil {
				return Generation{}, e
			}
			for _, chapter := range v.Chapters {
				if chapter.ID == draft.Document.Scope {
					add(r)
					found = true
				}
			}
		}
		if !found {
			return Generation{}, domain.ErrConflict
		}
	}
	for _, ref := range draft.Document.Inputs {
		r, e := resourceTx(ctx, tx, book.ProjectID, ref.ID)
		if e != nil {
			return Generation{}, e
		}
		if r.BranchID != book.BranchID || planPin(r) != ref {
			return Generation{}, domain.ErrConflict
		}
		add(r)
	}
	working := []WorkingRef{{Kind: "resource_draft", ID: resource, Version: strconv.FormatInt(draft.Revision, 10)}}
	// Creation-time characters may still be an author's private IP draft.
	ipDraft, e := ipDraftTx(ctx, tx, actor, book.ProjectID, book.BranchID)
	if e != nil {
		return Generation{}, e
	}
	if ipDraft.Revision > 0 && ipDraft.BaseHead == check.head {
		working = append(working, WorkingRef{Kind: "ip_draft", ID: book.BranchID, Version: strconv.FormatInt(ipDraft.Revision, 10)})
	}
	tx.Rollback()
	if _, err = s.Knowledge.Status(ctx, a, book.ProjectID, book.BranchID); errors.Is(err, domain.ErrNotReady) {
		_, err = s.Knowledge.Build(ctx, a, book.ProjectID, book.BranchID)
	}
	if err != nil {
		return Generation{}, err
	}
	query := []rune(in.Notes)
	if len(query) > 1000 {
		query = query[:1000]
	}
	pack, err := s.Knowledge.Search(ctx, a, book.ProjectID, book.BranchID, SearchInput{Query: string(query), Purpose: "author", Limit: 8, TokenBudget: 131072, IncludeWorking: true, RequiredResourceIDs: required, WorkingRefs: working})
	if err != nil {
		return Generation{}, err
	}
	bundle, err := presets.Bundle("novel-planning")
	if err != nil {
		return Generation{}, err
	}
	role := "planner"
	if draft.Document.Kind == "world" {
		role = "architect"
	}
	input, err := json.Marshal(map[string]any{"kind": draft.Document.Kind, "request": in.Notes, "current": draft.Document, "settings": book.Setup})
	if err != nil {
		return Generation{}, err
	}
	generation := GenerationInput{novelPlanningFingerprint: fingerprint, BranchID: book.BranchID, ExpectedIPHead: pack.IPHead, Role: role, ContextID: pack.ID, PriceID: in.PriceID, OperationID: in.OperationID, Request: models.Request{System: string(bundle.Find("SKILL.md")), Prompt: string(input), Schema: bundle.Find("output.schema.json"), MaxOutputTokens: in.MaxOutputTokens}}
	if in.AgentVersionID != "" {
		generation.AgentVersionID = in.AgentVersionID
		generation.AgentInput = input
		generation.CapabilityTrial = true
		generation.Request.System = "根据作者当前要求和服务端固定上下文提出规划修改建议。修改身份、来源版本、权限或确认状态不属于模型权限。所选方法是明确试用，不代表质量资格。"
		generation.Request.Schema = nil
	}
	return s.Generations.Prepare(ctx, a, book.ProjectID, generation)
}

// All planning drafts in an author context keep their novel settings/IP basis
// valid at preparation, dispatch and proposal review; no model call is made here.
func (s *IP) validateNovelPlanningContextTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.Role == "character_designer" {
		return s.validateCharacterDesignContextTx(ctx, tx, g)
	}
	if g.Frozen.ContextID == "" || (g.Frozen.Role != "planner" && g.Frozen.Role != "architect") {
		return nil
	}
	pack, err := (&Knowledge{IP: s}).readContextTx(ctx, tx, g.ActorID, g.ProjectID, g.BranchID, g.Frozen.ContextID)
	if err != nil {
		return err
	}
	var check *planningCheck
	for _, item := range pack.Unconfirmed {
		if item.Ref.Kind != "resource_draft" {
			continue
		}
		r, e := resourceTx(ctx, tx, g.ProjectID, item.Ref.ID)
		if e != nil {
			return e
		}
		if !isNovelPlan(r.Kind) {
			continue
		}
		if check == nil {
			check, e = newPlanningCheck(ctx, tx, s, g.ProjectID, g.BranchID)
			if e != nil {
				return e
			}
		}
		if len(item.Parts) == 0 {
			return domain.ErrNotReady
		}
		doc, e := decodeNovelPlan([]byte(item.Parts[0].Text))
		if e != nil {
			return e
		}
		if e = check.validateSettings(doc); e != nil {
			return e
		}
	}
	return nil
}

func (s *NovelPlanningAssistance) Suggestion(ctx context.Context, actor, bookID, resource, taskID string) (NovelPlanningSuggestion, error) {
	var result NovelPlanningSuggestion
	plans := &NovelPlanning{IP: s.Generations.IP}
	tx, a, book, err := plans.begin(ctx, actor, bookID, true)
	if err != nil {
		return result, err
	}
	tx.Rollback()
	g, err := s.Generations.Read(ctx, a, book.ProjectID, taskID)
	if err != nil {
		return result, err
	}
	if g.ActorID != actor || g.BranchID != book.BranchID || g.Cancelled || g.State != "SUCCEEDED" || g.Output == nil || g.Frozen.ContextID == "" {
		return result, domain.ErrConflict
	}
	tx, a, book, err = plans.begin(ctx, actor, bookID, true)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	pack, err := s.Knowledge.readContextTx(ctx, tx, actor, book.ProjectID, book.BranchID, g.Frozen.ContextID)
	if err != nil {
		return result, err
	}
	draft, check, err := plans.draftTx(ctx, tx, a, book, resource)
	if err != nil {
		return result, err
	}
	if err = check.validateSettings(draft.Document); err != nil {
		return result, err
	}
	role := "planner"
	if draft.Document.Kind == "world" {
		role = "architect"
	}
	if g.Frozen.Role != role {
		return result, domain.ErrValidation
	}
	found := false
	for _, item := range pack.Unconfirmed {
		if item.Ref.Kind == "resource_draft" && item.Ref.ID == resource && item.Ref.Version == strconv.FormatInt(draft.Revision, 10) {
			found = true
		}
	}
	if !found {
		return result, domain.ErrConflict
	}
	proposal, p, err := novel.ApplyPlanningProposal(draft.Document, []byte(g.Output.Text), func(kind string, index int) string {
		return digest(taskID + "\x00" + kind + "\x00" + strconv.Itoa(index))[:32]
	})
	if err != nil {
		return result, fmt.Errorf("%w：助手输出不符合当前规划契约", err)
	}
	draft.Document = proposal
	draft.Ready = false
	draft.Reason = "助手建议尚未保存，请对照修改后再确认规划"
	result = NovelPlanningSuggestion{Draft: draft, Explanation: p.Explanation, Questions: p.Questions}
	return result, nil
}
