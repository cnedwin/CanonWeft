package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
)

const characterBatchKind = "novel-character-batch-v1"

type CharacterBatchInput struct {
	Revision          int64  `json:"revision"`
	Hash              string `json:"hash"`
	Notes             string `json:"notes"`
	Size              int    `json:"size"`
	AgentVersionID    string `json:"agent_version_id,omitempty"`
	PriceID           string `json:"price_id"`
	Currency          string `json:"currency"`
	BudgetLimitMicros int64  `json:"budget_limit_micros"`
	MaxOutputTokens   int    `json:"max_output_tokens"`
	OperationID       string `json:"operation_id"`
}
type characterBatchRoot struct {
	Kind       string              `json:"kind"`
	BookID     string              `json:"book_id"`
	ResourceID string              `json:"resource_id"`
	IPRevision int64               `json:"ip_revision"`
	IDs        []string            `json:"ids"`
	Genre      string              `json:"genre"`
	ContextID  string              `json:"context_id"`
	Request    CharacterBatchInput `json:"request"`
}
type characterBatchStep struct {
	BookID     string                     `json:"book_id"`
	ResourceID string                     `json:"resource_id"`
	IDs        []string                   `json:"ids"`
	Notes      string                     `json:"notes"`
	Genre      string                     `json:"genre"`
	Previous   map[string]json.RawMessage `json:"previous"`
}

func characterBatchKey(i int) string { return fmt.Sprintf("characters-%d", i+1) }

func (s *NovelCharacters) InstallBatchAgent(ctx context.Context, actor, bookID, id string) (domain.CapabilityRef, error) {
	if _, err := s.Read(ctx, actor, bookID, id); err != nil {
		return domain.CapabilityRef{}, err
	}
	return s.installAgent(ctx, actor, bookID, "", "character-ensemble-batch")
}

func (s *NovelCharacters) PrepareBatch(ctx context.Context, actor, bookID, id string, in CharacterBatchInput) (RecipeExecution, error) {
	var empty RecipeExecution
	if !validName(in.OperationID, 90) || !validName(in.Notes, 10000) || in.Revision < 1 || in.Hash == "" || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 || in.BudgetLimitMicros < 1 {
		return empty, domain.ErrValidation
	}
	tx, a, book, err := s.begin(ctx, actor, bookID)
	if err != nil {
		return empty, err
	}
	defer tx.Rollback()
	prior, _, e := requestReceiptTx(ctx, tx, a, "recipe_runs", in.OperationID, false)
	if e == nil {
		r, e := recipeTx(ctx, tx, book.ProjectID, prior)
		if e != nil {
			return empty, e
		}
		var root characterBatchRoot
		if json.Unmarshal(r.Frozen.Input, &root) != nil || root.Kind != characterBatchKind || root.BookID != bookID || root.ResourceID != id || capabilityDigest(root.Request) != capabilityDigest(in) {
			return empty, domain.ErrConflict
		}
		return r, nil
	}
	if e != sql.ErrNoRows {
		return empty, e
	}
	d, w, _, err := s.readTx(ctx, tx, a, book, id)
	if err != nil {
		return empty, err
	}
	if d.Revision != in.Revision || d.Hash != in.Hash {
		return empty, domain.ErrConflict
	}
	if err = validateCharacterSettings(ctx, tx, book.ProjectID, book.BranchID, w); err != nil {
		return empty, err
	}
	chunks, err := novel.CharacterChunks(d.Document.IDs, in.Size)
	if err != nil {
		return empty, fmt.Errorf("%w：每段1至5人，单次最多32段；请适当增加每段人数", err)
	}
	search, err := characterSearchTx(ctx, tx, book, d)
	if err != nil {
		return empty, err
	}
	tx.Rollback()
	pack, err := s.characterContext(ctx, a, book, search, in.Notes)
	if err != nil {
		return empty, err
	}
	agent, err := s.installAgent(ctx, actor, bookID, in.AgentVersionID, "character-ensemble-batch")
	if err != nil {
		return empty, err
	}
	steps := []domain.RecipeStep{}
	inputs := []RecipeStepInput{}
	for i, ids := range chunks {
		key := characterBatchKey(i)
		step := domain.RecipeStep{Key: key, AgentID: agent.ID, After: []string{}}
		payload := characterBatchStep{BookID: bookID, ResourceID: id, IDs: ids, Notes: in.Notes, Genre: d.Document.Genre, Previous: map[string]json.RawMessage{}}
		bindings := []domain.RecipeValueBinding{}
		for j := 0; j < i; j++ {
			priorKey := characterBatchKey(j)
			step.After = append(step.After, priorKey)
			payload.Previous[priorKey] = json.RawMessage(`{}`)
			bindings = append(bindings, domain.RecipeValueBinding{TargetPointer: "/previous/" + priorKey, SourceStep: priorKey, SourcePointer: ""})
		}
		steps = append(steps, step)
		raw, _ := json.Marshal(payload)
		inputs = append(inputs, RecipeStepInput{Key: key, PriceID: in.PriceID, MaxOutputTokens: in.MaxOutputTokens, ContextID: pack.ID, Input: raw, Bindings: bindings})
	}
	key := fmt.Sprintf("character-batch-%d-%s", len(chunks), agent.ID)
	prefix := "preset." + key + "." + book.ProjectID
	recipe, err := (&Capabilities{IP: s.Generations.IP, presetStep: prefix}).Save(ctx, a, book.ProjectID, CapabilitySave{OperationID: prefix, Definition: domain.CapabilityDefinition{Kind: "recipe", Key: key, Name: fmt.Sprintf("群像分段创作·%d段", len(chunks)), Description: "逐段保留人物候选，承接全部前段设定；失败可重试。", Instructions: "保持稳定人物身份，逐段完整生成后汇总，不自动采用或发布。", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`), Dependencies: []domain.CapabilityRef{agent}, Steps: steps}})
	if err != nil {
		return empty, err
	}
	root := characterBatchRoot{Kind: characterBatchKind, BookID: bookID, ResourceID: id, IPRevision: d.IPRevision, IDs: slices.Clone(d.Document.IDs), Genre: d.Document.Genre, ContextID: pack.ID, Request: in}
	raw, _ := json.Marshal(root)
	return (&RecipeExecutions{Generations: s.Generations}).Prepare(ctx, a, book.ProjectID, RecipeExecutionInput{RecipeVersionID: recipe.ID, BranchID: book.BranchID, ExpectedIPHead: pack.IPHead, OperationID: in.OperationID, Trial: true, Input: raw, Currency: in.Currency, BudgetLimitMicros: in.BudgetLimitMicros, Steps: inputs})
}

func characterSearchTx(ctx context.Context, tx *sql.Tx, book NovelBook, d CharacterDesignDraft) (SearchInput, error) {
	in := SearchInput{Purpose: "author", Limit: 8, TokenBudget: 131072, IncludeWorking: true, WorkingRefs: []WorkingRef{{Kind: "resource_draft", ID: d.ResourceID, Version: strconv.FormatInt(d.Revision, 10)}}}
	if err := validateCharacterSourcesTx(ctx, tx, book, d.Document.SourceIDs); err != nil {
		return in, err
	}
	in.RequiredResourceIDs = slices.Clone(d.Document.SourceIDs)
	if d.IPRevision > 0 {
		in.WorkingRefs = append(in.WorkingRefs, WorkingRef{Kind: "ip_draft", ID: book.BranchID, Version: strconv.FormatInt(d.IPRevision, 10)})
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM resources WHERE project_id=? AND branch_id=? AND kind IN ('novel_plan_world','novel_plan_book') AND head_revision<>'' ORDER BY id`, book.ProjectID, book.BranchID)
	if err != nil {
		return in, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return in, err
		}
		in.RequiredResourceIDs = append(in.RequiredResourceIDs, id)
	}
	return in, rows.Err()
}

func (s *NovelCharacters) characterContext(ctx context.Context, a domain.Actor, book NovelBook, in SearchInput, notes string) (domain.ContextPackage, error) {
	_, err := s.Knowledge.Status(ctx, a, book.ProjectID, book.BranchID)
	if errors.Is(err, domain.ErrNotReady) {
		_, err = s.Knowledge.Build(ctx, a, book.ProjectID, book.BranchID)
	}
	if err != nil {
		return domain.ContextPackage{}, err
	}
	query := []rune(notes)
	if len(query) > 1000 {
		query = query[:1000]
	}
	in.Query = string(query)
	return s.Knowledge.Search(ctx, a, book.ProjectID, book.BranchID, in)
}

func validateCharacterBatchSteps(r RecipeExecution, root characterBatchRoot) error {
	chunks, err := novel.CharacterChunks(root.IDs, root.Request.Size)
	if err != nil {
		return err
	}
	seen := 0
	for _, step := range r.Steps {
		if !step.Active {
			continue
		}
		if step.Ordinal != seen || seen >= len(chunks) || step.Key != characterBatchKey(seen) || step.Frozen.Agent.Definition.Role != "character_designer" || step.Frozen.Input.ContextID != root.ContextID {
			return domain.ErrValidation
		}
		var input characterBatchStep
		if json.Unmarshal(step.Frozen.Input.Input, &input) != nil || input.BookID != root.BookID || input.ResourceID != root.ResourceID || !slices.Equal(input.IDs, chunks[seen]) || input.Notes != root.Request.Notes || input.Genre != root.Genre || len(input.Previous) != seen || len(step.Frozen.Step.After) != seen || len(step.Frozen.Input.Bindings) != seen {
			return domain.ErrValidation
		}
		for i := 0; i < seen; i++ {
			key := characterBatchKey(i)
			binding := step.Frozen.Input.Bindings[i]
			if step.Frozen.Step.After[i] != key || binding.SourceStep != key || binding.SourcePointer != "" || binding.TargetPointer != "/previous/"+key || string(input.Previous[key]) != "{}" {
				return domain.ErrValidation
			}
		}
		seen++
	}
	if seen != len(chunks) {
		return domain.ErrValidation
	}
	return nil
}

func (s *RecipeExecutions) validateCharacterBatchTx(ctx context.Context, tx *sql.Tx, r RecipeExecution) error {
	var root characterBatchRoot
	_ = json.Unmarshal(r.Frozen.Input, &root)
	if root.Kind != characterBatchKind {
		for _, step := range r.Steps {
			if step.Frozen.Agent.Definition.Key == "character-ensemble-batch" {
				return domain.ErrValidation
			}
		}
		return nil
	}
	if err := validateCharacterBatchSteps(r, root); err != nil {
		return err
	}
	a := domain.Actor{ID: r.ActorID, WorkspaceID: r.WorkspaceID}
	book, err := readNovelTx(ctx, tx, a, root.BookID)
	if err != nil {
		return err
	}
	if book.ProjectID != r.ProjectID || book.BranchID != r.BranchID {
		return domain.ErrForbidden
	}
	d, w, _, err := (&NovelCharacters{Generations: s.Generations}).readTx(ctx, tx, a, book, root.ResourceID)
	if err != nil {
		return err
	}
	if d.Revision != root.Request.Revision || d.Hash != root.Request.Hash || d.IPRevision != root.IPRevision || !slices.Equal(d.Document.IDs, root.IDs) || d.Document.Genre != root.Genre || d.BaseHead != r.Frozen.BaseHead {
		return domain.ErrConflict
	}
	if err = validateCharacterSettings(ctx, tx, r.ProjectID, r.BranchID, w); err != nil {
		return err
	}
	pack, err := (&Knowledge{IP: s.Generations.IP}).readContextTx(ctx, tx, r.ActorID, r.ProjectID, r.BranchID, root.ContextID)
	if err != nil {
		return err
	}
	if err = validateCharacterSourceContext(d.Document.SourceIDs, pack); err != nil {
		return err
	}
	for _, item := range pack.Unconfirmed {
		if item.Ref.Kind == "resource_draft" && item.Ref.ID == root.ResourceID && item.Ref.Version == strconv.FormatInt(d.Revision, 10) {
			return nil
		}
	}
	return domain.ErrConflict
}
