package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
)

func (s *Memory) summarySourcesTx(ctx context.Context, tx *sql.Tx, project, branch string, parent memory.Item, memo map[string]memorySourceInfo) ([]domain.KnowledgePolicy, error) {
	policies := []domain.KnowledgePolicy{}
	if parent.Summary == nil {
		return policies, nil
	}
	for _, pin := range parent.Summary.Inputs {
		key := "memory:" + pin.ID
		info, ok := memo[key]
		if !ok {
			if len(memo) >= 8192 {
				return nil, domain.ErrContextBudget
			}
			child, err := memoryItemTx(ctx, tx, "", project, branch, pin.ID)
			if err != nil {
				return nil, err
			}
			if memory.ValidateItem(child) != nil {
				return nil, domain.ErrNotReady
			}
			info.Item = &child
			memo[key] = info
		}
		child := *info.Item
		if child.Revision != pin.Revision || memory.SummaryTextHash(child.Text) != pin.Hash || child.State == "candidate" || child.State == "revoked" {
			return nil, domain.ErrConflict
		}
		if memory.ValidateSummarySource(parent, child, pin) != nil {
			return nil, domain.ErrValidation
		}
		if !info.Checked {
			_, info.Err = s.validateSourcesMemo(ctx, tx, project, branch, child, memo)
			info.Checked = true
			memo[key] = info
		}
		if info.Err != nil {
			return nil, info.Err
		}
	}
	return policies, nil
}

// Historical provenance may remain readable after a source changes, but only
// under current project access and the revision's original private ownership.
func (s *Memory) ReadRevision(ctx context.Context, a domain.Actor, project, branch, id, revision string) (memory.Item, error) {
	var item memory.Item
	if !lowerHex32(id) || !lowerHex32(revision) {
		return item, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return item, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return item, err
	}
	// A new private edit must not revoke another member's access to a previously
	// shared revision. Check current project/branch ownership separately; the
	// requested frozen revision below supplies its own private actor boundary.
	var currentID string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM memory_items WHERE project_id=? AND branch_id=? AND id=?", project, branch, id).Scan(&currentID); err == sql.ErrNoRows {
		return item, domain.ErrForbidden
	} else if err != nil {
		return item, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, "SELECT document FROM memory_revisions WHERE project_id=? AND item_id=? AND id=?", project, id, revision).Scan(&raw); err == sql.ErrNoRows {
		return item, domain.ErrForbidden
	}
	if err != nil {
		return item, err
	}
	if err := rejectExpiredRuntime(raw, "memory_revisions.document"); err != nil {
		return item, err
	}
	if json.Unmarshal(raw, &item) != nil || memory.ValidateItem(item) != nil {
		return item, domain.ErrNotReady
	}
	if item.Project != project || item.Branch != branch || item.ID != id || item.Revision != revision || (item.Actor != "" && item.Actor != a.ID) {
		return memory.Item{}, domain.ErrForbidden
	}
	return item, nil
}
