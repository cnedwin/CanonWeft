package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

type ImportDraft struct {
	Revision int64                     `json:"revision"`
	Content  domain.ImportDraftContent `json:"content"`
	Stale    bool                      `json:"stale"`
}

func importDraftTx(ctx context.Context, tx *sql.Tx, actor, project, branch, id string) (ImportDraft, error) {
	var d ImportDraft
	b, err := branchTx(ctx, tx, project, branch)
	if err != nil {
		return d, err
	}
	source, err := importTx(ctx, tx, actor, project, branch, id)
	if err != nil {
		return d, err
	}
	var raw []byte
	var hash string
	err = tx.QueryRowContext(ctx, "SELECT revision,payload,content_hash FROM import_drafts WHERE import_id=? AND actor_id=? AND project_id=? FOR UPDATE", id, actor, project).Scan(&d.Revision, &raw, &hash)
	if err == sql.ErrNoRows {
		d.Content = domain.ImportDraftContent{BaseIPHead: b.Head, Text: source.Report.Text, OperationID: "import-draft-" + id, Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}
		if source.Report.Research != nil {
			d.Content.Policy.Space = "research"
		}
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if d.Revision < 1 || digest(string(raw)) != hash || json.Unmarshal(raw, &d.Content) != nil || d.Content.Validate() != nil {
		return d, domain.ErrNotReady
	}
	d.Stale = d.Content.BaseIPHead != b.Head
	return d, nil
}
func (s *IP) ReadImportDraft(ctx context.Context, a domain.Actor, project, branch, id string) (ImportDraft, error) {
	var d ImportDraft
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	return importDraftTx(ctx, tx, a.ID, project, branch, id)
}
func (s *IP) SaveImportDraft(ctx context.Context, a domain.Actor, project, branch, id string, d ImportDraft) (ImportDraft, error) {
	if d.Revision < 0 || d.Revision == math.MaxInt64 || d.Content.Validate() != nil {
		return d, domain.ErrValidation
	}
	raw, _ := json.Marshal(d.Content)
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
	current, err := importDraftTx(ctx, tx, a.ID, project, branch, id)
	if err != nil {
		return d, err
	}
	if d.Revision != current.Revision {
		old, _ := json.Marshal(current.Content)
		if current.Revision == d.Revision+1 && digest(string(old)) == hash {
			return current, nil
		}
		return d, domain.ErrConflict
	}
	source := retention.SourceKey{Table: "import_drafts", Primary: map[string]string{"import_id": id, "actor_id": a.ID}, IfPresent: true}
	if err = s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	d.Revision++
	d.Stale = d.Content.BaseIPHead != b.Head
	_, err = tx.ExecContext(ctx, "INSERT INTO import_drafts(import_id,actor_id,project_id,revision,payload,content_hash) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),payload=VALUES(payload),content_hash=VALUES(content_hash),updated_at=UTC_TIMESTAMP(6)", id, a.ID, project, d.Revision, raw, hash)
	if err != nil {
		return d, err
	}
	source.IfPresent = false
	if err = s.captureSourceValuesTx(ctx, tx, project, source); err != nil {
		return d, err
	}
	return d, tx.Commit()
}
