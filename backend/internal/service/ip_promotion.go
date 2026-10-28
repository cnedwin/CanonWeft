package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
)

type PromotionInput = domain.PromotionInput

func (s *IP) ProposePromotion(ctx context.Context, a domain.Actor, project, branch string, input PromotionInput) (Candidate, error) {
	if !validName(input.Reason, 2000) || input.ChangeReasons == nil {
		return Candidate{}, domain.ErrValidation
	}
	return s.propose(ctx, a, project, branch, input.BaseHead, input.OperationID, domain.Snapshot{}, nil, "", &input, input.ChangeReasons)
}

func (s *IP) PromotionDeltaPreview(ctx context.Context, a domain.Actor, project, branch string, input PromotionInput) (domain.BranchDelta, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	if b.Head != input.BaseHead {
		return domain.BranchDelta{}, domain.ErrConflict
	}
	next, err := promotionSnapshotTx(ctx, tx, project, branch, input)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	base, err := snapshotTx(ctx, tx, project, b.Head)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	normalizeDeltaMetadata(base, &next)
	return domain.PreviewBranchDelta(base, next)
}
func promotionSnapshotTx(ctx context.Context, tx *sql.Tx, project, target string, input PromotionInput) (domain.Snapshot, error) {
	source, err := branchTx(ctx, tx, project, input.SourceBranch)
	if err != nil {
		return domain.Snapshot{}, err
	}
	if source.ParentID != target || source.Head != input.SourceHead || input.SourceHead == "" {
		return domain.Snapshot{}, domain.ErrConflict
	}
	parent, err := snapshotTx(ctx, tx, project, input.BaseHead)
	if err != nil {
		return parent, err
	}
	child, err := snapshotTx(ctx, tx, project, input.SourceHead)
	if err != nil {
		return parent, err
	}
	return domain.SelectPromotion(parent, child, input.Selection)
}
func loadPromotion(ctx context.Context, tx *sql.Tx, c *Candidate) error {
	var raw []byte
	err := tx.QueryRowContext(ctx, "SELECT payload FROM ip_promotions WHERE candidate_id=?", c.ID).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var input PromotionInput
	if err := rejectExpiredRuntime(raw, "ip_promotions.payload"); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &input); err != nil {
		return err
	}
	c.Promotion = &input
	return nil
}
