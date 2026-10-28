package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type PatchDraft struct {
	Revision int64                     `json:"revision"`
	Content  *domain.PatchDraftContent `json:"content"`
	Stale    bool                      `json:"stale"`
}

func patchDraftTx(ctx context.Context, tx *sql.Tx, actor, project, branch string) (PatchDraft, error) {
	var d PatchDraft
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT revision,payload,content_hash FROM patch_drafts WHERE branch_id=? AND actor_id=? AND project_id=? FOR UPDATE", branch, actor, project).Scan(&d.Revision, &raw, &hash)
	if err == sql.ErrNoRows {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if d.Revision < 1 || digest(string(raw)) != hash || json.Unmarshal(raw, &d.Content) != nil || d.Content == nil || d.Content.Validate() != nil {
		return d, domain.ErrNotReady
	}
	return d, nil
}

// Reading preserves old anchors: a stale private draft is recoverable work,
// never a new candidate or an implicit permission to overwrite a newer head.
func (s *IP) ReadPatchDraft(ctx context.Context, a domain.Actor, project, branch string) (PatchDraft, error) {
	var d PatchDraft
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return d, err
	}
	d, err = patchDraftTx(ctx, tx, a.ID, project, branch)
	if err != nil || d.Content == nil {
		return d, err
	}
	d.Stale = d.Content.BaseIPHead != b.Head
	for _, p := range d.Content.Group.Patches {
		r, e := resourceTx(ctx, tx, project, p.ResourceID)
		if e != nil {
			return d, e
		}
		if r.BranchID != branch {
			return d, domain.ErrForbidden
		}
		d.Stale = d.Stale || r.Head != p.BaseRevision || r.Hash != p.BaseHash
	}
	return d, nil
}

func (s *IP) SavePatchDraft(ctx context.Context, a domain.Actor, project, branch string, d PatchDraft) (PatchDraft, error) {
	if d.Revision < 0 || d.Revision == math.MaxInt64 || d.Content == nil || d.Content.Validate() != nil {
		return d, domain.ErrValidation
	}
	raw, err := json.Marshal(d.Content)
	if err != nil {
		return d, err
	}
	hash := digest(string(raw))
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return d, err
	}
	if _, err = snapshotTx(ctx, tx, project, d.Content.BaseIPHead); err != nil {
		return d, err
	}
	d.Stale = d.Content.BaseIPHead != b.Head
	ids := append([]string{}, d.Content.Group.Members...)
	if d.Content.SelectedResourceID != "" {
		ids = append(ids, d.Content.SelectedResourceID)
	}
	for _, id := range ids {
		r, e := resourceTx(ctx, tx, project, id)
		if e != nil {
			return d, e
		}
		if r.BranchID != branch {
			return d, domain.ErrForbidden
		}
		for _, patch := range d.Content.Group.Patches {
			if patch.ResourceID == id && (patch.BaseRevision != r.Head || patch.BaseHash != r.Hash) {
				d.Stale = true
			}
		}
	}
	for _, id := range []string{d.Content.CompensationSourceID, d.Content.Group.CompensatesReleaseID} {
		if id == "" {
			continue
		}
		r, e := releaseTx(ctx, tx, project, id)
		if e != nil {
			return d, e
		}
		if r.Manifest.BranchID != branch || r.Status != "PUBLISHED" {
			return d, domain.ErrForbidden
		}
	}
	current, err := patchDraftTx(ctx, tx, a.ID, project, branch)
	if err != nil {
		return d, err
	}
	if current.Revision != d.Revision {
		// Retry only the immediately preceding identical save after a lost reply.
		saved, _ := json.Marshal(current.Content)
		if current.Revision == d.Revision+1 && digest(string(saved)) == hash {
			current.Stale = d.Stale
			return current, nil
		}
		return d, domain.ErrConflict
	}
	source := retention.SourceKey{Table: "patch_drafts", Primary: map[string]string{"branch_id": branch, "actor_id": a.ID}, IfPresent: true}
	if err = s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	d.Revision++
	_, err = tx.ExecContext(ctx, "INSERT INTO patch_drafts(branch_id,actor_id,project_id,revision,payload,content_hash) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),payload=VALUES(payload),content_hash=VALUES(content_hash),updated_at=CURRENT_TIMESTAMP(6)", branch, a.ID, project, d.Revision, raw, hash)
	if err != nil {
		return d, err
	}
	source.IfPresent = false
	if err = s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	return d, tx.Commit()
}
