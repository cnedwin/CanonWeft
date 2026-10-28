package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"novelstudio/internal/domain"
	"slices"
	"strconv"
)

type WorkingRef = domain.WorkingRef
type WorkingChoice struct {
	Ref   WorkingRef `json:"ref"`
	Name  string     `json:"name"`
	State string     `json:"state"`
}
type WorkingChoices struct {
	Items      []WorkingChoice `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
}
type WorkingPart = domain.WorkingPart
type WorkingInput = domain.WorkingInput

func validateWorkingRefs(input SearchInput) error {
	if len(input.WorkingRefs) == 0 {
		return nil
	}
	if len(input.WorkingRefs) > 16 || !input.IncludeWorking || !slices.Contains([]string{"author", "critic", "research"}, input.Purpose) {
		return domain.ErrValidation
	}
	seen := map[string]bool{}
	for _, ref := range input.WorkingRefs {
		key := ref.Kind + ":" + ref.ID
		if seen[key] || !validName(ref.ID, 128) || !validName(ref.Version, 128) || !slices.Contains([]string{"resource_draft", "ip_draft", "ip_candidate", "release_candidate"}, ref.Kind) {
			return domain.ErrValidation
		}
		seen[key] = true
	}
	return nil
}

// Lists metadata only; private drafts are always scoped to the authenticated
// actor, even when a project owner is inspecting another person's project work.
func (s *Knowledge) WorkingChoices(ctx context.Context, a domain.Actor, project, branch, after string) (WorkingChoices, error) {
	result := WorkingChoices{Items: []WorkingChoice{}}
	if len(after) > 180 {
		return result, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return result, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT kind,id,name,version,state FROM (
 SELECT 'resource_draft' AS kind,r.id,r.name,CAST(d.revision AS CHAR) AS version,'PRIVATE_DRAFT' AS state
 FROM resource_drafts d JOIN resources r ON r.id=d.resource_id WHERE r.project_id=? AND r.branch_id=? AND d.actor_id=?
 UNION ALL SELECT 'ip_draft',branch_id,?,CAST(revision AS CHAR),'PRIVATE_DRAFT' FROM ip_drafts WHERE branch_id=? AND actor_id=?
 UNION ALL SELECT 'ip_candidate',id,CONCAT('设定候选 · ',CAST(created_at AS CHAR)),content_hash,status FROM ip_revisions WHERE branch_id=? AND status IN ('PREPARED','READY')
 UNION ALL SELECT 'release_candidate',id,CONCAT('资料候选 · ',CAST(created_at AS CHAR)),content_hash,status FROM artifact_releases WHERE project_id=? AND branch_id=? AND status IN ('PREPARED','READY')
 ) choices WHERE CONCAT(kind,':',id)>? ORDER BY kind,id LIMIT 101`, project, branch, a.ID, b.Name+" · 设定草稿", branch, a.ID, branch, project, branch, after)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item WorkingChoice
		if err = rows.Scan(&item.Ref.Kind, &item.Ref.ID, &item.Name, &item.Ref.Version, &item.State); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 100 {
		result.Items = result.Items[:100]
		last := result.Items[99].Ref
		result.NextCursor = last.Kind + ":" + last.ID
	}
	return result, nil
}

func (s *Knowledge) workingInputs(ctx context.Context, tx *sql.Tx, actor, project, branch string, input SearchInput) ([]WorkingInput, error) {
	if len(input.WorkingRefs) == 0 {
		return nil, nil
	}
	if err := validateWorkingRefs(input); err != nil {
		return nil, err
	}
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return nil, err
	}
	result := []WorkingInput{}
	bytes := 0
	for _, ref := range input.WorkingRefs {
		item, err := s.workingInput(ctx, tx, actor, project, b, ref)
		if err != nil {
			return nil, err
		}
		for _, part := range item.Parts {
			bytes += len(part.Text)
		}
		if bytes*2 > input.TokenBudget {
			return nil, fmt.Errorf("所选未确认资料超过上下文容量，未截断: %w", domain.ErrContextBudget)
		}
		result = append(result, item)
	}
	return result, nil
}

func (s *Knowledge) workingInput(ctx context.Context, tx *sql.Tx, actor, project string, b Branch, ref WorkingRef) (WorkingInput, error) {
	item := WorkingInput{Ref: ref, Parts: []WorkingPart{}}
	add := func(name, format string, value any) {
		text := ""
		if format == "text" {
			text = value.(string)
		} else {
			raw, _ := json.Marshal(value)
			text = string(raw)
		}
		item.Parts = append(item.Parts, WorkingPart{Name: name, Format: format, Text: text})
	}
	switch ref.Kind {
	case "resource_draft":
		r, err := resourceTx(ctx, tx, project, ref.ID)
		if err != nil {
			return item, err
		}
		if r.BranchID != b.ID {
			return item, domain.ErrForbidden
		}
		d, err := resourceDraftTx(ctx, tx, actor, project, ref.ID)
		if err != nil {
			return item, err
		}
		if d.Revision == 0 {
			return item, domain.ErrForbidden
		}
		if strconv.FormatInt(d.Revision, 10) != ref.Version || d.BaseHead != r.Head {
			return item, domain.ErrConflict
		}
		item.State = "PRIVATE_DRAFT"
		item.BaseHead = d.BaseHead
		add(r.Name, "text", d.Text)
		if d.Policy != nil {
			add("拟定用途（未发布）", "structured", d.Policy)
		}
	case "ip_draft":
		if ref.ID != b.ID {
			return item, domain.ErrForbidden
		}
		d, err := ipDraftTx(ctx, tx, actor, project, b.ID)
		if err != nil {
			return item, err
		}
		if d.Revision == 0 {
			return item, domain.ErrForbidden
		}
		if strconv.FormatInt(d.Revision, 10) != ref.Version || d.BaseHead != b.Head {
			return item, domain.ErrConflict
		}
		item.State = "PRIVATE_DRAFT"
		item.BaseHead = d.BaseHead
		add("未确认的设定草稿", "structured", d)
	case "ip_candidate":
		c, err := candidateTx(ctx, tx, b.ID, ref.ID)
		if err != nil {
			return item, err
		}
		if c.BaseHead != b.Head || c.Hash != ref.Version || !slices.Contains([]string{"PREPARED", "READY"}, c.Status) {
			return item, domain.ErrConflict
		}
		if valueHash(c.Snapshot) != c.Hash {
			return item, domain.ErrNotReady
		}
		item.State = c.Status
		item.BaseHead = c.BaseHead
		add("未发布的设定候选", "structured", c)
	case "release_candidate":
		r, err := releaseTx(ctx, tx, project, ref.ID)
		if err != nil {
			return item, err
		}
		if r.Manifest.BranchID != b.ID {
			return item, domain.ErrForbidden
		}
		if r.Hash != ref.Version || !slices.Contains([]string{"PREPARED", "READY"}, r.Status) {
			return item, domain.ErrConflict
		}
		if err = s.IP.validateManifest(ctx, tx, project, r.Manifest); err != nil {
			return item, err
		}
		item.State = r.Status
		item.BaseHead = r.Manifest.ExpectedIPHead
		add("本组拟定来源与依赖（不授予发布权限）", "structured", r.Manifest)
		for _, f := range r.Manifest.Artifacts {
			source, err := resourceTx(ctx, tx, project, f.ResourceID)
			if err != nil {
				return item, err
			}
			raw, err := s.IP.Blobs.ReadObject(ctx, project, f.Hash)
			if err != nil {
				return item, err
			}
			add(source.Name, "text", string(raw))
		}
		if r.Manifest.IPCandidateID != "" {
			c, err := candidateTx(ctx, tx, b.ID, r.Manifest.IPCandidateID)
			if err != nil {
				return item, err
			}
			if valueHash(c.Snapshot) != c.Hash {
				return item, domain.ErrNotReady
			}
			add("同批设定候选", "structured", c)
		}
	default:
		return item, domain.ErrValidation
	}
	return item, nil
}
