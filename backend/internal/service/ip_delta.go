package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
)

func (s *IP) ProposeChanges(ctx context.Context, a domain.Actor, project, branch, base, operation string, snapshot domain.Snapshot, reasons map[string]string) (Candidate, error) {
	if reasons == nil {
		return Candidate{}, domain.ErrValidation
	}
	return s.propose(ctx, a, project, branch, base, operation, snapshot, nil, "", nil, reasons)
}

func (s *IP) ProposeAmendmentChanges(ctx context.Context, a domain.Actor, project, branch, base, operation, reason string, snapshot domain.Snapshot, reasons map[string]string) (Candidate, error) {
	if reasons == nil || !validName(reason, 2000) {
		return Candidate{}, domain.ErrValidation
	}
	return s.propose(ctx, a, project, branch, base, operation, snapshot, nil, reason, nil, reasons)
}

func normalizeDeltaMetadata(base domain.Snapshot, next *domain.Snapshot) {
	assertions := map[string]string{}
	for _, v := range base.Assertions {
		assertions[v.ID] = v.RecordedAt
	}
	for i := range next.Assertions {
		next.Assertions[i].RecordedAt = assertions[next.Assertions[i].ID]
	}
	rules := map[string]string{}
	for _, v := range base.Rules {
		rules[v.ID] = v.RecordedAt
	}
	for i := range next.Rules {
		next.Rules[i].RecordedAt = rules[next.Rules[i].ID]
	}
	events := map[string]string{}
	for _, v := range base.Events {
		events[v.ID] = v.RecordedAt
	}
	for i := range next.Events {
		next.Events[i].RecordedAt = events[next.Events[i].ID]
	}
}

func (s *IP) DeltaPreview(ctx context.Context, a domain.Actor, project, branch, base string, snapshot domain.Snapshot) (domain.BranchDelta, error) {
	tx, _, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	defer tx.Rollback()
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	if b.Head != base {
		return domain.BranchDelta{}, domain.ErrConflict
	}
	previous, err := snapshotTx(ctx, tx, project, base)
	if err != nil {
		return domain.BranchDelta{}, err
	}
	normalizeDeltaMetadata(previous, &snapshot)
	return domain.PreviewBranchDelta(previous, snapshot)
}

func loadCandidateDelta(ctx context.Context, tx *sql.Tx, c *Candidate) error {
	var merges int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ip_merge_candidates WHERE candidate_id=?", c.ID).Scan(&merges); err != nil {
		return err
	}
	c.RequiresSemanticReview = c.Promotion != nil || merges > 0
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT payload,delta_hash FROM ip_branch_deltas WHERE candidate_id=?", c.ID).Scan(&raw, &hash)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := rejectExpiredRuntime(raw, "ip_branch_deltas.payload"); err != nil {
		return err
	}
	if digest(string(raw)) != hash {
		return domain.ErrNotReady
	}
	var delta domain.BranchDelta
	if json.Unmarshal(raw, &delta) != nil || delta.Schema != 1 {
		return domain.ErrNotReady
	}
	c.Delta = &delta
	return nil
}

func saveCandidateDelta(ctx context.Context, tx *sql.Tx, a domain.Actor, project string, b Branch, c *Candidate, previous domain.Snapshot, reasons map[string]string, merge *MergeInput, promotion *PromotionInput) error {
	preview, err := domain.PreviewBranchDelta(previous, c.Snapshot)
	if err != nil {
		return err
	}
	augmented := map[string]string{}
	for key, value := range reasons {
		augmented[key] = value
	}
	for _, op := range preview.Operations {
		if op.Field == "/recorded_at" {
			augmented[op.Key()] = "程序记录本次变更时间"
		}
	}
	delta, err := domain.BuildBranchDelta(previous, c.Snapshot, augmented)
	if err != nil {
		return err
	}
	for i := range delta.Operations {
		source := domain.DeltaSource{Kind: "manual", ID: a.ID, Revision: c.ID, Hash: c.Hash}
		if merge != nil {
			source = domain.DeltaSource{Kind: "merge", ID: b.ParentID, Revision: merge.UpstreamHead}
		}
		if promotion != nil {
			source = domain.DeltaSource{Kind: "promotion", ID: promotion.SourceBranch, Revision: promotion.SourceHead}
		}
		if source.Revision != "" && (merge != nil || promotion != nil) {
			if err = tx.QueryRowContext(ctx, "SELECT content_hash FROM ip_revisions WHERE id=?", source.Revision).Scan(&source.Hash); err != nil {
				return err
			}
		}
		if delta.Operations[i].Field == "/recorded_at" {
			source.Kind = "server_timestamp"
		}
		delta.Operations[i].SourceRefs = []domain.DeltaSource{source}
		if b.Head != "" {
			var priorHash string
			if err = tx.QueryRowContext(ctx, "SELECT content_hash FROM ip_revisions WHERE id=?", b.Head).Scan(&priorHash); err != nil {
				return err
			}
			delta.Operations[i].SourceRefs = append(delta.Operations[i].SourceRefs, domain.DeltaSource{Kind: "previous", ID: b.ID, Revision: b.Head, Hash: priorHash})
		}
	}
	payload, err := json.Marshal(delta)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO ip_branch_deltas(candidate_id,project_id,branch_id,base_revision,parent_revision,delta_hash,payload,created_by) VALUES(?,?,?,?,?,?,?,?)", c.ID, project, b.ID, b.BaseRevision, b.Head, digest(string(payload)), string(payload), a.ID); err != nil {
		return err
	}
	c.Delta = &delta
	return nil
}
