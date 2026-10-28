package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/presets"
)

type MemoryImportInput struct {
	RunID            string   `json:"run_id"`
	StepID           string   `json:"step_id"`
	ExpectedRevision int64    `json:"expected_revision"`
	OutputHash       string   `json:"output_hash"`
	SessionID        string   `json:"session_id"`
	Timeline         string   `json:"timeline"`
	CandidateIDs     []string `json:"candidate_ids"`
}
type extractionAnchor struct {
	ResourceID string `json:"resource_id"`
	Revision   string `json:"revision"`
	Hash       string `json:"hash"`
	Start      int    `json:"start_rune"`
	End        int    `json:"end_rune"`
}
type extractionCandidate struct {
	ID            string             `json:"id"`
	Kind          string             `json:"kind"`
	Layer         string             `json:"layer"`
	Subject       string             `json:"subject_id"`
	Holder        string             `json:"holder_id"`
	Object        string             `json:"object_id"`
	Predicate     string             `json:"predicate"`
	Statement     string             `json:"statement"`
	Context       string             `json:"context"`
	SummaryLevel  string             `json:"summary_level"`
	NarrativeRole string             `json:"narrative_role"`
	Disposition   string             `json:"disposition"`
	RelatedIDs    []string           `json:"related_ids"`
	CheckResult   string             `json:"check_result"`
	Scope         extractionScope    `json:"scope"`
	Evidence      []extractionAnchor `json:"evidence"`
}

// Keep every semantic distinction visible in editable text. Raw structured
// claims and quotes remain pinned in the workflow output; they never confer
// authority on entities, facts, visibility, or mandatory author anchors.
func extractionItem(c extractionCandidate, sources map[string]extractionSource) memory.Item {
	kind := c.Kind
	control := c.Context == "plan" || c.Kind == "theme_check"
	if kind == "foreshadow" {
		kind = "promise"
	}
	if kind == "theme_check" {
		kind = "summary"
	}
	if slices.Contains([]string{"dream", "hypothetical"}, c.Context) {
		kind = "hypothesis"
	}
	if c.Context == "plan" && !slices.Contains([]string{"promise", "highlight", "summary"}, kind) {
		kind = "intent"
	}
	item := memory.Item{Kind: kind, Visibility: "author", From: c.Scope.StoryTime, RevealAt: c.Scope.KnownFrom, Control: control, State: "candidate"}
	for _, anchor := range c.Evidence {
		source := sources[anchor.ResourceID]
		if source.Kind != "memory" {
			item.Sources = append(item.Sources, memory.Source{Resource: anchor.ResourceID, Revision: anchor.Revision, Hash: anchor.Hash, Start: anchor.Start, End: anchor.End})
		}
		if source.Until != nil && (item.Until == nil || *source.Until < *item.Until) {
			until := *source.Until
			item.Until = &until
		}
		if source.Kind == "planning" || source.Control {
			item.Control = true
		}
	}
	contextLabel := map[string]string{"reality": "现实", "dream": "梦境", "flashback": "回忆", "hypothetical": "假设", "plan": "未发生的计划"}[c.Context]
	item.Text = fmt.Sprintf("%s\n\n语境：%s；原类型：%s；层级：%s；摘要层级：%s；叙事作用：%s。\n主体：%s；信念持有人：%s；对象：%s；关系：%s。\n审核提示：%s；主题核对：%s；关联候选：%s。", c.Statement, contextLabel, c.Kind, c.Layer, c.SummaryLevel, c.NarrativeRole, c.Subject, c.Holder, c.Object, c.Predicate, c.Disposition, c.CheckResult, strings.Join(c.RelatedIDs, "、"))
	return item
}

// ImportExtraction imports selected proposal IDs atomically into a private
// checkpoint. The caller cannot submit model output or overwrite an old import.
func (s *Memory) ImportExtraction(ctx context.Context, a domain.Actor, project, branch string, in MemoryImportInput) ([]memory.Item, error) {
	if !lowerHex32(in.RunID) || !lowerHex32(in.StepID) || !lowerHex32(in.SessionID) || len(in.OutputHash) != 64 || in.ExpectedRevision < 1 || !validName(in.Timeline, 160) || len(in.CandidateIDs) < 1 || len(in.CandidateIDs) > 128 {
		return nil, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, project, in.RunID)
	if err != nil {
		return nil, err
	}
	if r.ActorID != a.ID || r.BranchID != branch {
		return nil, domain.ErrForbidden
	}
	if r.Revision != in.ExpectedRevision || r.CancelRequested || r.RestoredDisabled || r.Paused || !slices.Contains([]string{"WAITING_CONFIRMATION", "COMPLETED"}, r.State) {
		return nil, domain.ErrConflict
	}
	step, err := recipeStepByID(r, in.StepID)
	if err != nil {
		return nil, err
	}
	if !step.Active || step.OutputHash != in.OutputHash || !slices.Contains([]string{"WAITING_CONFIRMATION", "COMPLETED"}, step.State) || step.Frozen.Agent.Definition.Key != "shared-memory-consolidation" || len(step.Frozen.Input.Bindings) != 0 {
		return nil, domain.ErrConflict
	}
	if _, err = memorySessionTx(ctx, tx, a.ID, project, branch, in.SessionID); err != nil {
		return nil, err
	}
	var frozen extractionDocument
	if json.Unmarshal(step.Frozen.Input.Input, &frozen) != nil {
		return nil, domain.ErrNotReady
	}
	if frozen.SummaryTarget != nil && frozen.SummaryTarget.ItemID != "" {
		if len(in.CandidateIDs) != 1 {
			return nil, domain.ErrValidation
		}
		id := in.CandidateIDs[0]
		op := "memory.extract." + valueHash([]string{project, r.ID, step.ID, id})
		hash := valueHash([]string{project, branch, r.ID, step.ID, step.OutputHash, id, in.SessionID, in.Timeline})
		if priorHash, priorID, receiptErr := operationReceiptTx(ctx, tx, a, op); receiptErr == nil {
			if priorHash != hash || priorID != frozen.SummaryTarget.ItemID {
				return nil, domain.ErrConflict
			}
			prior, loadErr := memoryItemTx(ctx, tx, a.ID, project, branch, priorID)
			return []memory.Item{prior}, loadErr
		} else if receiptErr != sql.ErrNoRows {
			return nil, receiptErr
		}
	}
	if err = s.validateExtractionInputTx(ctx, tx, a.ID, project, branch, r.Frozen.BaseHead, step.Frozen.Input.Input); err != nil {
		return nil, err
	}
	raw, err := s.IP.Blobs.ReadObject(ctx, project, step.OutputHash)
	if err != nil || digest(string(raw)) != step.OutputHash {
		return nil, domain.ErrNotReady
	}
	if err = presets.ValidateMemoryProposal("memory-consolidation", step.Frozen.Input.Input, raw); err != nil {
		return nil, domain.ErrValidation
	}
	var proposal struct {
		Candidates []extractionCandidate `json:"candidates"`
		Findings   []struct {
			Description  string             `json:"description"`
			Question     string             `json:"question"`
			CandidateIDs []string           `json:"candidate_ids"`
			Evidence     []extractionAnchor `json:"evidence"`
		} `json:"findings"`
		Limitations []string `json:"limitations"`
	}
	var input extractionDocument
	if json.Unmarshal(raw, &proposal) != nil || json.Unmarshal(step.Frozen.Input.Input, &input) != nil {
		return nil, domain.ErrNotReady
	}
	if input.SummaryTarget != nil && input.SummaryTarget.Timeline != in.Timeline {
		return nil, domain.ErrValidation
	}
	replacement, err := s.refreshTargetTx(ctx, tx, a.ID, project, branch, input.SummaryTarget)
	if err != nil {
		return nil, err
	}
	sources := map[string]extractionSource{}
	for _, source := range input.Sources {
		sources[source.ResourceID] = source
	}
	candidates := map[string]extractionCandidate{}
	for _, candidate := range proposal.Candidates {
		candidates[candidate.ID] = candidate
	}
	result := make([]memory.Item, 0, len(in.CandidateIDs))
	seen := map[string]bool{}
	sourceMemo := map[string]memorySourceInfo{}
	for _, id := range in.CandidateIDs {
		c, exists := candidates[id]
		if !exists || seen[id] {
			return nil, domain.ErrValidation
		}
		seen[id] = true
		op := "memory.extract." + valueHash([]string{project, r.ID, step.ID, id})
		hash := valueHash([]string{project, branch, r.ID, step.ID, step.OutputHash, id, in.SessionID, in.Timeline})
		if priorHash, priorID, receiptErr := operationReceiptTx(ctx, tx, a, op); receiptErr == nil {
			if hash != priorHash {
				return nil, domain.ErrConflict
			}
			prior, loadErr := memoryItemTx(ctx, tx, a.ID, project, branch, priorID)
			if loadErr != nil {
				return nil, loadErr
			}
			result = append(result, prior)
			continue
		} else if receiptErr != sql.ErrNoRows {
			return nil, receiptErr
		}
		// Review findings are part of the proposed memory, including their source
		// dependencies. Do not discard contradictions when importing a short claim.
		review := ""
		for _, finding := range proposal.Findings {
			if len(finding.CandidateIDs) == 0 || slices.Contains(finding.CandidateIDs, id) {
				review += "\n待核对：" + finding.Description + "\n待确认问题：" + finding.Question
				c.Evidence = append(c.Evidence, finding.Evidence...)
			}
		}
		for _, limit := range proposal.Limitations {
			review += "\n提炼限制：" + limit
		}
		item := extractionItem(c, sources)
		if target := input.SummaryTarget; target != nil {
			item.Summary = &memory.Summary{Level: target.Level, UnitID: target.UnitID, Title: target.Title}
			for _, source := range input.Sources {
				if source.Kind == "memory" {
					item.Summary.Inputs = append(item.Summary.Inputs, memory.SummaryPin{ID: source.ResourceID, Revision: source.Revision, Hash: source.Hash})
				}
			}
		}
		item.Text += review
		for _, anchor := range c.Evidence {
			item.RevealAt = max(item.RevealAt, sources[anchor.ResourceID].Scope.KnownFrom)
		}
		item.ID, item.Revision, item.Project, item.Branch, item.Actor, item.Session, item.Timeline = NewID(), NewID(), project, branch, a.ID, in.SessionID, in.Timeline
		parent := ""
		if replacement != nil {
			item = preserveRefreshPolicy(item, *replacement)
			parent = replacement.Revision
		}
		if err = memory.ValidateItem(item); err != nil {
			return nil, domain.ErrValidation
		}
		if _, err = s.validateSourcesMemo(ctx, tx, project, branch, item, sourceMemo); err != nil {
			return nil, err
		}
		if err = s.persist(ctx, tx, a, item, parent, op, hash); err != nil {
			return nil, err
		}
		if replacement != nil {
			if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'memory.update',?,?)", a.ID, refreshImportOperation(r, step), refreshImportHash(r, step), item.ID); err != nil {
				return nil, err
			}
		}
		result = append(result, item)
	}
	return result, tx.Commit()
}
