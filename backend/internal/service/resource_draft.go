package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"unicode/utf8"
)

type ResourceDraft struct {
	historyReason string
	historySource *chapterHistorySource
	Policy        *domain.KnowledgePolicy `json:"policy,omitempty"`
	Revision      int64                   `json:"revision"`
	BaseHead      string                  `json:"base_head"`
	Text          string                  `json:"text"`
}

func (s *IP) ReadResourceDraft(ctx context.Context, a domain.Actor, project, id string) (ResourceDraft, error) {
	var draft ResourceDraft
	tx, a, _, err := s.begin(ctx, a, project, false)
	if err != nil {
		return draft, err
	}
	defer tx.Rollback()
	return resourceDraftTx(ctx, tx, a.ID, project, id)
}

func resourceDraftTx(ctx context.Context, tx *sql.Tx, actor, project, id string) (ResourceDraft, error) {
	var draft ResourceDraft
	var err error
	if _, err = resourceTx(ctx, tx, project, id); err != nil {
		return draft, err
	}
	err = tx.QueryRowContext(ctx, "SELECT revision,base_head,body FROM resource_drafts WHERE resource_id=? AND actor_id=? FOR UPDATE", id, actor).Scan(&draft.Revision, &draft.BaseHead, &draft.Text)
	if err == sql.ErrNoRows {
		return draft, nil
	}
	if err != nil {
		return draft, err
	}
	var raw []byte
	var policyRevision int64
	err = tx.QueryRowContext(ctx, "SELECT revision,payload FROM resource_policy_drafts WHERE resource_id=? AND actor_id=? FOR UPDATE", id, actor).Scan(&policyRevision, &raw)
	if err == sql.ErrNoRows {
		return draft, nil
	}
	if err != nil {
		return draft, err
	}
	if policyRevision != draft.Revision || json.Unmarshal(raw, &draft.Policy) != nil {
		return draft, domain.ErrNotReady
	}
	return draft, err
}
func (s *IP) SaveResourceDraft(ctx context.Context, a domain.Actor, project, id string, draft ResourceDraft) (ResourceDraft, error) {
	tx, a, _, err := s.begin(ctx, a, project, true)
	if err != nil {
		return draft, err
	}
	defer tx.Rollback()
	draft, err = s.saveResourceDraftTx(ctx, tx, a, project, id, draft)
	if err != nil {
		return draft, err
	}
	return draft, tx.Commit()
}

func (s *IP) saveResourceDraftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, project, id string, draft ResourceDraft) (ResourceDraft, error) {
	if draft.Revision < 0 || len(draft.Text) > 256*1024 || !utf8.ValidString(draft.Text) {
		return draft, domain.ErrValidation
	}
	if draft.Policy != nil && draft.Policy.Validate() != nil {
		return draft, domain.ErrValidation
	}
	r, err := resourceTx(ctx, tx, project, id)
	if err != nil {
		return draft, err
	}
	if r.Kind == chapterHistoryKind || manuscriptPrivateKind(r.Kind) {
		return draft, domain.ErrForbidden
	}
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT revision FROM resource_drafts WHERE resource_id=? AND actor_id=? FOR UPDATE", id, a.ID).Scan(&revision)
	if err != nil && err != sql.ErrNoRows {
		return draft, err
	}
	if revision != draft.Revision {
		return draft, domain.ErrConflict
	}
	if err = s.checkpointChapterSaveTx(ctx, tx, a, project, r, draft); err != nil {
		return draft, err
	}
	sources := []retention.SourceKey{
		{Table: "resource_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}, IfPresent: true},
		{Table: "resource_policy_drafts", Primary: map[string]string{"resource_id": id, "actor_id": a.ID}, IfPresent: true},
	}
	if err := s.captureSourceValuesTx(ctx, tx, project, sources...); err != nil {
		return draft, err
	}
	draft.Revision++
	_, err = tx.ExecContext(ctx, "INSERT INTO resource_drafts(resource_id,actor_id,revision,base_head,body) VALUES(?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),base_head=VALUES(base_head),body=VALUES(body),updated_at=CURRENT_TIMESTAMP(6)", id, a.ID, draft.Revision, draft.BaseHead, draft.Text)
	if err != nil {
		return draft, err
	}
	raw, err := json.Marshal(draft.Policy)
	if err != nil {
		return draft, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO resource_policy_drafts(resource_id,actor_id,revision,payload) VALUES(?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),payload=VALUES(payload)", id, a.ID, draft.Revision, raw)
	if err != nil {
		return draft, err
	}
	for i := range sources {
		sources[i].IfPresent = false
	}
	if err := s.captureSourceValuesTx(ctx, tx, project, sources...); err != nil {
		return draft, err
	}
	return draft, nil
}
