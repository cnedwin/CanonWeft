package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// These fields preserve an unfinished edit, never a permission or review grant.
// Candidate submission revalidates every referenced head and selected value.
type IPBranchActionDraft = domain.IPBranchActionDraft

type IPDraft = domain.IPDraft
type storedIPDraft = domain.StoredIPDraft

func (s *IP) ReadDraft(ctx context.Context, a domain.Actor, project, branch string) (IPDraft, error) {
	d := IPDraft{Snapshot: domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}}
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	return ipDraftTx(ctx, tx, a.ID, project, branch)
}

func ipDraftTx(ctx context.Context, tx *sql.Tx, actor, project, branch string) (IPDraft, error) {
	d := IPDraft{Snapshot: domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}}
	var err error
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return d, err
	}
	var payload []byte
	err = tx.QueryRowContext(ctx, "SELECT revision,base_head,payload FROM ip_drafts WHERE branch_id=? AND actor_id=? FOR UPDATE", branch, actor).Scan(&d.Revision, &d.BaseHead, &payload)
	if err == sql.ErrNoRows {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	return domain.DecodeIPDraft(payload, d.Revision, d.BaseHead)
}
func (s *IP) SaveDraft(ctx context.Context, a domain.Actor, project, branch string, d IPDraft) (IPDraft, error) {
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	d, err = s.saveIPDraftTx(ctx, tx, a, project, branch, d)
	if err != nil {
		return d, err
	}
	return d, tx.Commit()
}

// Shared by author edits and atomic character selection; the caller already
// holds the project authority transaction and commits all selected identities.
func (s *IP) saveIPDraftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, branch string, d IPDraft) (IPDraft, error) {
	if d.Revision < 0 || len(d.AmendmentReason) > 6000 {
		return d, domain.ErrValidation
	}
	for key, reason := range d.ChangeReasons {
		if len(key) > 512 || len(reason) > 4000 {
			return d, domain.ErrValidation
		}
	}
	payload, err := json.Marshal(storedIPDraft{DraftSchema: 2, Snapshot: d.Snapshot, Amendment: d.Amendment, AmendmentReason: d.AmendmentReason, ChangeReasons: d.ChangeReasons, BranchActions: d.BranchActions})
	if err != nil {
		return d, err
	}
	if len(payload) > 512*1024 {
		return d, domain.ErrValidation
	}
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return d, err
	}
	if _, err = snapshotTx(ctx, tx, project, d.BaseHead); err != nil {
		return d, err
	}
	var current int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM ip_drafts WHERE branch_id=? AND actor_id=? FOR UPDATE", branch, a.ID).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return d, err
	}
	if current != d.Revision {
		return d, domain.ErrConflict
	}
	source := retention.SourceKey{Table: "ip_drafts", Primary: map[string]string{"branch_id": branch, "actor_id": a.ID}, IfPresent: true}
	if err := s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	d.Revision++
	_, err = tx.ExecContext(ctx, "INSERT INTO ip_drafts(branch_id,actor_id,revision,base_head,payload) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),base_head=VALUES(base_head),payload=VALUES(payload),updated_at=CURRENT_TIMESTAMP(6)", branch, a.ID, d.Revision, d.BaseHead, payload)
	if err != nil {
		return d, err
	}
	source.IfPresent = false
	if err := s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	return d, nil
}
