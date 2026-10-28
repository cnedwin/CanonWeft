package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"slices"
	"unicode/utf8"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/presets"
)

// ExtractionSourceInput names resources, never supplies their text or policy.
type ExtractionSourceInput struct {
	MemoryIDs      []string       `json:"memory_ids,omitempty"`
	SummaryTarget  *SummaryTarget `json:"summary_target,omitempty"`
	ResourceIDs    []string       `json:"resource_ids"`
	ExpectedIPHead string         `json:"expected_ip_head"`
	StoryTime      int64          `json:"story_time"`
	Focus          []string       `json:"focus"`
	Instructions   string         `json:"instructions"`
}
type SummaryTarget struct {
	ItemID   string `json:"item_id,omitempty"`
	Revision string `json:"revision,omitempty"`
	Level    string `json:"level"`
	UnitID   string `json:"unit_id"`
	Title    string `json:"title"`
	Timeline string `json:"timeline"`
}
type extractionScope struct {
	BranchID      string   `json:"branch_id"`
	StoryTime     int64    `json:"story_time"`
	KnownFrom     int64    `json:"known_from"`
	PerspectiveID string   `json:"perspective_id"`
	Visibility    string   `json:"visibility"`
	KnownBy       []string `json:"known_by"`
}
type extractionSource struct {
	Control    bool            `json:"control,omitempty"`
	Until      *int64          `json:"until,omitempty"`
	ResourceID string          `json:"resource_id"`
	Revision   string          `json:"revision"`
	Hash       string          `json:"hash"`
	Kind       string          `json:"kind"`
	Text       string          `json:"text"`
	Scope      extractionScope `json:"scope"`
}
type extractionDocument struct {
	SummaryTarget *SummaryTarget     `json:"summary_target,omitempty"`
	Schema        int                `json:"schema"`
	ProjectID     string             `json:"project_id"`
	BaseRevision  string             `json:"base_revision"`
	Scope         extractionScope    `json:"scope"`
	Sources       []extractionSource `json:"sources"`
	Focus         []string           `json:"focus"`
	Instructions  string             `json:"instructions"`
}

func (s *Memory) extractionInputTx(ctx context.Context, tx *sql.Tx, actor, project, branch string, in ExtractionSourceInput) (extractionDocument, error) {
	out := extractionDocument{SummaryTarget: in.SummaryTarget, Schema: 1, ProjectID: project, BaseRevision: in.ExpectedIPHead, Focus: in.Focus, Instructions: in.Instructions}
	count := len(in.ResourceIDs) + len(in.MemoryIDs)
	if count < 1 || count > 128 || in.StoryTime < 0 || in.StoryTime > 9007199254740991 {
		return out, domain.ErrValidation
	}
	if in.SummaryTarget == nil && len(in.MemoryIDs) > 0 {
		return out, domain.ErrValidation
	}
	var refreshPrior *memory.Item
	if target := in.SummaryTarget; target != nil {
		rank := memory.SummaryRank(target.Level)
		if rank == 0 || !lowerHex32(target.UnitID) || !validName(target.Title, 160) || !validName(target.Timeline, 160) || len(target.Timeline) > 256 || (rank == 1 && len(in.MemoryIDs) > 0) || (rank >= 3 && len(in.MemoryIDs) == 0) || (len(in.MemoryIDs) > 0 && len(in.ResourceIDs) > 0) {
			return out, domain.ErrValidation
		}
		prior, err := s.refreshTargetTx(ctx, tx, actor, project, branch, target)
		if err != nil {
			return out, err
		}
		if prior != nil {
			refreshPrior = prior
			resources, summaries := refreshSourceIDs(*prior)
			if !sameRefreshIDs(resources, in.ResourceIDs) || !sameRefreshIDs(summaries, in.MemoryIDs) || in.StoryTime < max(prior.From, prior.RevealAt) || (prior.Until != nil && in.StoryTime >= *prior.Until) {
				return out, domain.ErrValidation
			}
		}
	}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return out, err
	}
	if b.Head != in.ExpectedIPHead {
		return out, domain.ErrConflict
	}
	if b.Head == "" {
		return out, domain.ErrNotReady
	}
	out.Scope = extractionScope{BranchID: branch, StoryTime: in.StoryTime, KnownFrom: in.StoryTime, PerspectiveID: "author", Visibility: "private", KnownBy: []string{"author"}}
	seen := map[string]bool{}
	total := 0
	for _, id := range in.ResourceIDs {
		if !lowerHex32(id) || seen[id] {
			return out, domain.ErrValidation
		}
		seen[id] = true
		resource, err := resourceTx(ctx, tx, project, id)
		if err != nil {
			return out, err
		}
		if resource.BranchID != branch {
			return out, domain.ErrForbidden
		}
		if resource.Head == "" {
			return out, domain.ErrNotReady
		}
		policy := domain.DefaultKnowledgePolicy()
		if resource.Policy != nil {
			policy = *resource.Policy
		}
		if !slices.Contains([]string{"evidence", "working"}, policy.Space) || policy.Validate() != nil {
			return out, domain.ErrValidation
		}
		if policy.RevealAt > in.StoryTime {
			return out, domain.ErrConflict
		}
		data, err := s.IP.Blobs.ReadObject(ctx, project, resource.Hash)
		if err != nil || digest(string(data)) != resource.Hash {
			return out, domain.ErrNotReady
		}
		total += len(data)
		if total > 192*1024 {
			return out, domain.ErrContextBudget
		}
		if !utf8.Valid(data) || len(data) == 0 {
			return out, domain.ErrValidation
		}
		kind := "chapter"
		if policy.Space == "working" {
			kind = "planning"
		}
		scope := out.Scope
		scope.KnownFrom = policy.RevealAt
		out.Sources = append(out.Sources, extractionSource{ResourceID: id, Revision: resource.Head, Hash: resource.Hash, Kind: kind, Text: string(data), Scope: scope})
	}
	memo := map[string]memorySourceInfo{}
	for _, id := range in.MemoryIDs {
		if !lowerHex32(id) || seen[id] {
			return out, domain.ErrValidation
		}
		seen[id] = true
		child, err := memoryItemTx(ctx, tx, "", project, branch, id)
		if err != nil {
			return out, err
		}
		pin := memory.SummaryPin{ID: child.ID, Revision: child.Revision, Hash: memory.SummaryTextHash(child.Text)}
		parent := memory.Item{Project: project, Branch: branch, Kind: "summary", Visibility: "author", From: in.StoryTime, RevealAt: in.StoryTime, Until: child.Until, Timeline: in.SummaryTarget.Timeline, Control: child.Control || child.State == "adopted_plan", Summary: &memory.Summary{Level: in.SummaryTarget.Level}}
		if memory.ValidateItem(child) != nil || memory.ValidateSummarySource(parent, child, pin) != nil || (child.Until != nil && in.StoryTime >= *child.Until) {
			return out, domain.ErrValidation
		}
		if _, err = s.validateSourcesMemo(ctx, tx, project, branch, child, memo); err != nil {
			return out, err
		}
		total += len(child.Text)
		if total > 192*1024 {
			return out, domain.ErrContextBudget
		}
		scope := out.Scope
		scope.KnownFrom = child.RevealAt
		out.Sources = append(out.Sources, extractionSource{ResourceID: id, Revision: child.Revision, Hash: pin.Hash, Kind: "memory", Text: child.Text, Scope: scope, Control: parent.Control, Until: child.Until})
	}
	if refreshPrior != nil {
		preview := *refreshPrior
		summary := *preview.Summary
		summary.Inputs = nil
		preview.Summary = &summary
		preview.Sources = nil
		preview.From, preview.RevealAt = in.StoryTime, in.StoryTime
		for i, source := range out.Sources {
			if refreshPrior.Control {
				out.Sources[i].Control = true
			}
			preview.Control = preview.Control || source.Control || source.Kind == "planning"
			if source.Until != nil && (preview.Until == nil || *source.Until < *preview.Until) {
				until := *source.Until
				preview.Until = &until
			}
			if source.Kind == "memory" {
				summary.Inputs = append(summary.Inputs, memory.SummaryPin{ID: source.ResourceID, Revision: source.Revision, Hash: source.Hash})
			} else {
				preview.Sources = append(preview.Sources, memory.Source{Resource: source.ResourceID, Revision: source.Revision, Hash: source.Hash, Start: 0, End: utf8.RuneCountInString(source.Text)})
			}
		}
		if memory.ValidateItem(preview) != nil {
			return out, domain.ErrValidation
		}
		if _, err = s.validateSourcesMemo(ctx, tx, project, branch, preview, memo); err != nil {
			return out, err
		}
	}
	raw, _ := json.Marshal(out)
	bundle, err := presets.Bundle("memory-consolidation")
	if err != nil {
		return out, domain.ErrNotReady
	}
	if len(raw) > 256*1024 {
		return out, domain.ErrContextBudget
	}
	if err = recipeContract(bundle.Find("input.schema.json"), raw); err != nil {
		return out, domain.ErrValidation
	}
	return out, nil
}

func (s *Memory) ExtractionInput(ctx context.Context, a domain.Actor, project, branch string, in ExtractionSourceInput) (json.RawMessage, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	doc, err := s.extractionInputTx(ctx, tx, a.ID, project, branch, in)
	if err != nil {
		return nil, err
	}
	return json.Marshal(doc)
}

// Reconstruct from authoritative current storage at preparation and at every
// dispatch gate. A plausible source hash supplied by a client is not authority.
func (s *Memory) validateExtractionInputTx(ctx context.Context, tx *sql.Tx, actor, project, branch, head string, raw json.RawMessage) error {
	var claimed extractionDocument
	if json.Unmarshal(raw, &claimed) != nil || claimed.ProjectID != project || claimed.BaseRevision != head || claimed.Scope.BranchID != branch {
		return domain.ErrValidation
	}
	ids := make([]string, 0, len(claimed.Sources))
	var memoryIDs []string
	for _, source := range claimed.Sources {
		if source.Kind == "memory" {
			memoryIDs = append(memoryIDs, source.ResourceID)
		} else {
			ids = append(ids, source.ResourceID)
		}
	}
	actual, err := s.extractionInputTx(ctx, tx, actor, project, branch, ExtractionSourceInput{ResourceIDs: ids, MemoryIDs: memoryIDs, SummaryTarget: claimed.SummaryTarget, ExpectedIPHead: head, StoryTime: claimed.Scope.StoryTime, Focus: claimed.Focus, Instructions: claimed.Instructions})
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, claimed) {
		return domain.ErrConflict
	}
	return nil
}
