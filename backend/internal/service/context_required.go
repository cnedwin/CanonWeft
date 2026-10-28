package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"novelstudio/internal/skills"
	"unicode/utf8"
)

// Required inputs are never ranked or trimmed. Historical and foreign-branch
// values remain attributed references, not additions to the current canon.
type RequiredInput = domain.RequiredInput

func validateRequiredIDs(ids []string) error {
	if len(ids) > 16 {
		return domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !validName(id, 128) || seen[id] {
			return domain.ErrValidation
		}
		seen[id] = true
	}
	return nil
}

func allowsRequiredPolicy(p *domain.KnowledgePolicy, input SearchInput) bool {
	policy := domain.DefaultKnowledgePolicy()
	if p != nil {
		policy = *p
	}
	return policy.Allows(input.Purpose, input.Perspective, input.StoryTime, input.IncludeWorking, input.IncludeMethods)
}

func (s *Knowledge) requiredInputs(ctx context.Context, tx *sql.Tx, project, branch string, input SearchInput) ([]RequiredInput, error) {
	if len(input.RequiredResourceIDs) == 0 {
		return nil, nil
	}
	if err := validateRequiredIDs(input.RequiredResourceIDs); err != nil {
		return nil, err
	}
	pending := []domain.Dependency{}
	for _, id := range input.RequiredResourceIDs {
		r, err := resourceTx(ctx, tx, project, id)
		if err != nil {
			return nil, err
		}
		if r.BranchID != branch {
			return nil, domain.ErrForbidden
		}
		if r.Head == "" {
			return nil, domain.ErrNotReady
		}
		pending = append(pending, domain.Dependency{Kind: "evidence", Reason: "本次任务明确要求完整纳入", Source: domain.DependencyRef{Kind: "resource", BranchID: branch, ID: id, Revision: r.Head, Hash: resourceNodeHash(r.Hash, r.Policy, r.Dependencies)}})
	}
	result := []RequiredInput{}
	seen := map[string]bool{}
	bytes := 0
	for len(pending) > 0 {
		d := pending[0]
		pending = pending[1:]
		// Check even repeated sources: a pinned edge must not weaken a second edge
		// that requires the current revision.
		if err := validateDependencySource(ctx, tx, project, d); err != nil {
			return nil, err
		}
		key := valueHash(d.Source) + fmt.Sprint(d.Pinned)
		if seen[key] {
			continue
		}
		seen[key] = true
		if len(seen) > 256 {
			return nil, fmt.Errorf("必要依赖超过 256 个版本，请缩小任务范围: %w", domain.ErrContextBudget)
		}
		item, next, err := s.requiredInput(ctx, tx, project, branch, input, d)
		if err != nil {
			return nil, err
		}
		bytes += len(item.Text)
		if bytes*2 > input.TokenBudget {
			return nil, fmt.Errorf("必要资料全文超过上下文容量，未截断: %w", domain.ErrContextBudget)
		}
		result = append(result, item)
		pending = append(pending, next...)
	}
	return result, nil
}

func (s *Knowledge) requiredInput(ctx context.Context, tx *sql.Tx, project, branch string, input SearchInput, d domain.Dependency) (RequiredInput, []domain.Dependency, error) {
	item := RequiredInput{Dependency: d, Space: "constraints"}
	ref := d.Source
	if (input.Purpose == "writer" || input.Purpose == "npc") && ref.BranchID != branch {
		return item, nil, domain.ErrForbidden
	}
	if ref.Kind == "resource" {
		current, err := resourceTx(ctx, tx, project, ref.ID)
		if err != nil {
			return item, nil, err
		}
		// Historical visibility never overrides a restriction on the current source.
		if !allowsRequiredPolicy(current.Policy, input) {
			return item, nil, domain.ErrForbidden
		}
		release, err := releaseTx(ctx, tx, project, ref.Revision)
		if err != nil {
			return item, nil, err
		}
		for _, f := range release.Manifest.Artifacts {
			if f.ResourceID != ref.ID {
				continue
			}
			if !allowsRequiredPolicy(f.Policy, input) {
				return item, nil, domain.ErrForbidden
			}
			raw, err := s.IP.Blobs.ReadObject(ctx, project, f.Hash)
			if err != nil {
				return item, nil, err
			}
			if !utf8.Valid(raw) {
				return item, nil, domain.ErrNotReady
			}
			item.Name = current.Name
			item.Text = string(raw)
			item.Space = "evidence"
			if f.Policy != nil {
				item.Space = f.Policy.Space
			}
			if f.Dependencies != nil {
				return item, f.Dependencies.Inputs, nil
			}
			return item, nil, nil
		}
		return item, nil, domain.ErrNotReady
	}
	if ref.Kind == "skill" {
		if !input.IncludeMethods || input.Purpose == "writer" || input.Purpose == "npc" {
			return item, nil, domain.ErrForbidden
		}
		v, err := skillTx(ctx, tx, project, ref.ID)
		if err != nil {
			return item, nil, err
		}
		raw, err := s.IP.Blobs.ReadObject(ctx, project, v.Report.Hash)
		if err != nil {
			return item, nil, err
		}
		var bundle skills.Bundle
		if json.Unmarshal(raw, &bundle) != nil || bundle.Validate() != nil {
			return item, nil, domain.ErrNotReady
		}
		canonical, _ := json.Marshal(bundle)
		if skills.Hash(canonical) != v.Report.Hash {
			return item, nil, domain.ErrNotReady
		}
		// All files are represented, including binary attachments by content hash.
		// This is reference material only; no script or requested tool is executed.
		text, err := skills.ReferenceText(bundle)
		if err != nil {
			return item, nil, domain.ErrNotReady
		}
		item.Name = v.Report.Name
		item.Space = "methods"
		item.Text = text
		return item, nil, nil
	}
	snapshot, err := snapshotTx(ctx, tx, project, ref.Revision)
	if err != nil {
		return item, nil, err
	}
	var value any
	switch ref.Kind {
	case "entity":
		for _, v := range snapshot.Entities {
			if v.ID == ref.ID {
				value = v
				item.Name = v.Name
			}
		}
	case "assertion":
		for _, v := range snapshot.Assertions {
			if v.ID == ref.ID {
				value = v
				item.Name = v.Subject + " · " + v.Predicate
			}
		}
	case "rule":
		for _, v := range snapshot.Rules {
			if v.ID == ref.ID {
				value = v
				item.Name = v.Name
			}
		}
	case "event":
		for _, v := range snapshot.Events {
			if v.ID == ref.ID {
				value = v
				item.Name = v.Name
			}
		}
	case "mapping":
		for _, v := range snapshot.Mappings {
			if v.ID == ref.ID {
				value = v
				item.Name = "实体映射"
			}
		}
	}
	if value == nil || valueHash(value) != ref.Hash {
		return item, nil, domain.ErrNotReady
	}
	if input.Purpose == "npc" {
		if ref.Kind != "assertion" || ref.BranchID != branch {
			return item, nil, domain.ErrForbidden
		}
		b, err := branchTx(ctx, tx, project, branch)
		if err != nil {
			return item, nil, err
		}
		current, err := snapshotTx(ctx, tx, project, b.Head)
		if err != nil {
			return item, nil, err
		}
		known := false
		for _, v := range domain.KnownFacts(current, input.Perspective, input.StoryTime) {
			if v.ID == ref.ID && valueHash(v) == ref.Hash {
				known = true
			}
		}
		if !known {
			return item, nil, domain.ErrForbidden
		}
	}
	item.Hidden = input.Purpose == "writer"
	encoded, _ := json.Marshal(value)
	item.Text = string(encoded)
	return item, nil, nil
}

func (s *Knowledge) validateRequiredContext(ctx context.Context, tx *sql.Tx, project, branch string, p ContextPackage) error {
	current, err := s.requiredInputs(ctx, tx, project, branch, p.Input)
	if err != nil {
		return err
	}
	if valueHash(current) != valueHash(p.Required) {
		return domain.ErrConflict
	}
	return nil
}
