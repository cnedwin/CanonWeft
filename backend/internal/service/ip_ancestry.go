package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
)

// A candidate's content is immutable, but its source branches may advance.
// Reuse this check before quoting, dispatching, adopting and publishing so a
// stale merge/promotion never earns a new review for obsolete source context.
func validateCandidateAncestryTx(ctx context.Context, tx *sql.Tx, project string, b Branch, c Candidate) (string, error) {
	if b.ID != c.BranchID || b.Head != c.BaseHead {
		return "", domain.ErrConflict
	}
	if c.Promotion != nil {
		source, err := branchTx(ctx, tx, project, c.Promotion.SourceBranch)
		if err != nil {
			return "", err
		}
		if source.ParentID != b.ID || source.Head != c.Promotion.SourceHead {
			return "", domain.ErrConflict
		}
	}
	var parentHead, oldBase string
	err := tx.QueryRowContext(ctx, "SELECT parent_head,old_base FROM ip_merge_candidates WHERE candidate_id=?", c.ID).Scan(&parentHead, &oldBase)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	parent, err := branchTx(ctx, tx, project, b.ParentID)
	if err != nil {
		return "", err
	}
	if parent.Head != parentHead || b.BaseRevision != oldBase {
		return "", domain.ErrConflict
	}
	return parentHead, nil
}
