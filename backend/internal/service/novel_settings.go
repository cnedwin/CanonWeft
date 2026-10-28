package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

type NovelSettingsDraft struct {
	Revision     int64       `json:"revision"`
	BaseRevision int64       `json:"base_revision"`
	Setup        novel.Setup `json:"setup"`
}
type NovelSettingsPreview struct {
	Hash            string                  `json:"hash"`
	DraftRevision   int64                   `json:"draft_revision"`
	BaseRevision    int64                   `json:"base_revision"`
	Changes         []novel.SettingChange   `json:"changes"`
	PlanningChanged bool                    `json:"planning_changed"`
	ReviewResources []NovelSettingsResource `json:"review_resources"`
}
type NovelSettingsResource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Head string `json:"head"`
}
type NovelSettingsConfirmation struct {
	DraftRevision    int64  `json:"draft_revision"`
	ExpectedRevision int64  `json:"expected_revision"`
	PreviewHash      string `json:"preview_hash"`
	OperationID      string `json:"operation_id"`
	AcceptSettings   bool   `json:"accept_settings"`
}
type NovelSettingsVersion struct {
	Revision int64       `json:"revision"`
	Setup    novel.Setup `json:"setup"`
}

func (s *NovelEntry) settingsTx(ctx context.Context, actor, id string, write bool) (*sql.Tx, domain.Actor, NovelBook, error) {
	tx, a, err := s.begin(ctx, actor, write)
	if err != nil {
		return nil, a, NovelBook{}, err
	}
	book, err := readNovelTx(ctx, tx, a, id)
	if err == nil && write {
		var p domain.Project
		p, err = projectTx(ctx, tx, a, book.ProjectID)
		if err == nil && !domain.CanEdit(p.Role) {
			err = domain.ErrForbidden
		}
	}
	if err != nil {
		tx.Rollback()
		return nil, a, book, err
	}
	// The project authority is locked before obtaining the current settings head.
	var revision int64
	err = tx.QueryRowContext(ctx, "SELECT settings_revision FROM novels WHERE id=? FOR UPDATE", id).Scan(&revision)
	if err == nil && revision != book.SettingsRevision {
		err = domain.ErrConflict
	}
	if err != nil {
		tx.Rollback()
		return nil, a, book, err
	}
	return tx, a, book, nil
}

func settingsVersionTx(ctx context.Context, tx *sql.Tx, id string, revision int64) (novel.Setup, error) {
	var setup novel.Setup
	var raw, hash string
	err := tx.QueryRowContext(ctx, "SELECT payload,content_hash FROM novel_settings_versions WHERE novel_id=? AND revision=?", id, revision).Scan(&raw, &hash)
	if err != nil {
		return setup, domain.ErrNotReady
	}
	if digest(raw) != hash || json.Unmarshal([]byte(raw), &setup) != nil || setup.Validate(true) != nil {
		return setup, domain.ErrNotReady
	}
	return setup, nil
}

func settingsDraftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, book NovelBook) (NovelSettingsDraft, error) {
	d := NovelSettingsDraft{BaseRevision: book.SettingsRevision, Setup: book.Setup}
	var raw, hash string
	err := tx.QueryRowContext(ctx, "SELECT revision,base_revision,payload,content_hash FROM novel_settings_drafts WHERE novel_id=? AND actor_id=? FOR UPDATE", book.ID, a.ID).Scan(&d.Revision, &d.BaseRevision, &raw, &hash)
	if err == sql.ErrNoRows {
		return d, nil
	}
	if err != nil {
		return d, err
	}
	if digest(raw) != hash || json.Unmarshal([]byte(raw), &d.Setup) != nil {
		return d, domain.ErrNotReady
	}
	return d, nil
}
func (s *NovelEntry) ReadSettingsDraft(ctx context.Context, actor, id string) (NovelSettingsDraft, error) {
	tx, a, book, err := s.settingsTx(ctx, actor, id, true)
	if err != nil {
		return NovelSettingsDraft{}, err
	}
	defer tx.Rollback()
	return settingsDraftTx(ctx, tx, a, book)
}
func (s *NovelEntry) SaveSettingsDraft(ctx context.Context, actor, id string, d NovelSettingsDraft) (NovelSettingsDraft, error) {
	if d.Revision < 0 || d.BaseRevision < 1 {
		return d, domain.ErrValidation
	}
	tx, a, book, err := s.settingsTx(ctx, actor, id, true)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	if d.BaseRevision != book.SettingsRevision {
		return d, domain.ErrConflict
	}
	if err = novel.ValidateSettingsEdit(book.Setup, d.Setup, false); err != nil {
		return d, err
	}
	current, err := settingsDraftTx(ctx, tx, a, book)
	if err != nil {
		return d, err
	}
	raw, err := json.Marshal(d.Setup)
	if err != nil || len(raw) > 1024*1024 {
		return d, domain.ErrValidation
	}
	if current.Revision == d.Revision+1 && current.BaseRevision == d.BaseRevision && valueHash(current.Setup) == valueHash(d.Setup) {
		return current, nil
	}
	if current.Revision != d.Revision {
		return d, domain.ErrConflict
	}
	ip := &IP{Workspace: s.Workspace}
	key := retention.SourceKey{Table: "novel_settings_drafts", Primary: map[string]string{"novel_id": id, "actor_id": a.ID}, IfPresent: true}
	if err = ip.captureSourceValuesTx(ctx, tx, book.ProjectID, key); err != nil {
		return d, err
	}
	d.Revision++
	_, err = tx.ExecContext(ctx, "INSERT INTO novel_settings_drafts(novel_id,actor_id,revision,base_revision,payload,content_hash) VALUES(?,?,?,?,?,?) ON DUPLICATE KEY UPDATE revision=VALUES(revision),base_revision=VALUES(base_revision),payload=VALUES(payload),content_hash=VALUES(content_hash),updated_at=UTC_TIMESTAMP(6)", id, a.ID, d.Revision, d.BaseRevision, raw, digest(string(raw)))
	if err != nil {
		return d, err
	}
	key.IfPresent = false
	if err = ip.captureSourceValuesTx(ctx, tx, book.ProjectID, key); err != nil {
		return d, err
	}
	if err = audit(ctx, tx, a, "novel.settings.save", id); err != nil {
		return d, err
	}
	return d, tx.Commit()
}

func settingsPreviewTx(ctx context.Context, tx *sql.Tx, book NovelBook, d NovelSettingsDraft) (NovelSettingsPreview, error) {
	p := NovelSettingsPreview{DraftRevision: d.Revision, BaseRevision: d.BaseRevision, ReviewResources: []NovelSettingsResource{}}
	if d.Revision < 1 || d.BaseRevision != book.SettingsRevision {
		return p, domain.ErrConflict
	}
	if err := novel.ValidateSettingsEdit(book.Setup, d.Setup, true); err != nil {
		return p, err
	}
	p.Changes = novel.SettingsChanges(book.Setup, d.Setup)
	if len(p.Changes) == 0 {
		return p, &novel.Validation{Fields: map[string]string{"settings": "设置没有变化"}}
	}
	for _, change := range p.Changes {
		p.PlanningChanged = p.PlanningChanged || change.Scope == "planning"
	}
	if p.PlanningChanged {
		rows, err := tx.QueryContext(ctx, "SELECT id,name,head_revision FROM resources WHERE project_id=? AND branch_id=? ORDER BY id LIMIT 1001", book.ProjectID, book.BranchID)
		if err != nil {
			return p, err
		}
		defer rows.Close()
		for rows.Next() {
			var item NovelSettingsResource
			if err = rows.Scan(&item.ID, &item.Name, &item.Head); err != nil {
				return p, err
			}
			p.ReviewResources = append(p.ReviewResources, item)
		}
		if err = rows.Err(); err != nil {
			return p, err
		}
		if len(p.ReviewResources) > 1000 {
			return p, domain.ErrContextBudget
		}
	}
	p.Hash = valueHash([]any{"novel.settings.preview.v1", book.ID, d, p.Changes, p.ReviewResources})
	return p, nil
}
func (s *NovelEntry) PreviewSettings(ctx context.Context, actor, id string, revision int64) (NovelSettingsPreview, error) {
	tx, a, book, err := s.settingsTx(ctx, actor, id, true)
	if err != nil {
		return NovelSettingsPreview{}, err
	}
	defer tx.Rollback()
	d, err := settingsDraftTx(ctx, tx, a, book)
	if err != nil {
		return NovelSettingsPreview{}, err
	}
	if d.Revision != revision {
		return NovelSettingsPreview{}, domain.ErrConflict
	}
	return settingsPreviewTx(ctx, tx, book, d)
}
func (s *NovelEntry) ConfirmSettings(ctx context.Context, actor, id string, in NovelSettingsConfirmation) (NovelBook, error) {
	if !in.AcceptSettings || in.ExpectedRevision < 1 || in.DraftRevision < 1 || len(in.PreviewHash) != 64 || !validName(in.OperationID, 120) {
		return NovelBook{}, domain.ErrValidation
	}
	tx, a, book, err := s.settingsTx(ctx, actor, id, true)
	if err != nil {
		return book, err
	}
	defer tx.Rollback()
	hash := valueHash([]any{"novel.settings.confirm.v1", id, in})
	stored, result, err := operationReceiptTx(ctx, tx, a, in.OperationID)
	if err == nil {
		if stored != hash || result != id {
			return book, domain.ErrConflict
		}
		book.SettingsRevision = in.ExpectedRevision + 1
		book.Setup, err = settingsVersionTx(ctx, tx, id, book.SettingsRevision)
		return book, err
	}
	if err != sql.ErrNoRows {
		return book, err
	}
	d, err := settingsDraftTx(ctx, tx, a, book)
	if err != nil {
		return book, err
	}
	if book.SettingsRevision != in.ExpectedRevision || d.Revision != in.DraftRevision {
		return book, domain.ErrConflict
	}
	p, err := settingsPreviewTx(ctx, tx, book, d)
	if err != nil {
		return book, err
	}
	if p.Hash != in.PreviewHash {
		return book, domain.ErrConflict
	}
	if d.Setup.ModelChoice == "configured" {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT v.config FROM model_connections c JOIN model_connection_versions v ON v.connection_id=c.id AND v.revision=c.revision WHERE c.id=? AND c.workspace_id=? AND v.credential_ref<>''", d.Setup.ConnectionID, a.WorkspaceID).Scan(&raw); err != nil {
			return book, domain.ErrValidation
		}
		var config domain.ModelConfig
		if json.Unmarshal(raw, &config) != nil || !config.Enabled {
			return book, domain.ErrValidation
		}
	}
	book.SettingsRevision++
	book.Setup = d.Setup
	raw, _ := json.Marshal(d.Setup)
	ip := &IP{Workspace: s.Workspace}
	key := retention.SourceKey{Table: "novel_settings_drafts", Primary: map[string]string{"novel_id": id, "actor_id": a.ID}}
	if err = ip.captureSourceValuesTx(ctx, tx, book.ProjectID, key); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO novel_settings_versions(novel_id,revision,payload,content_hash,created_by) VALUES(?,?,?,?,?)", id, book.SettingsRevision, raw, digest(string(raw)), a.ID); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE novels SET settings_revision=? WHERE id=?", book.SettingsRevision, id); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE novel_settings_drafts SET revision=revision+1,base_revision=?,updated_at=UTC_TIMESTAMP(6) WHERE novel_id=? AND actor_id=?", book.SettingsRevision, id, a.ID); err != nil {
		return book, err
	}
	if err = ip.captureSourceValuesTx(ctx, tx, book.ProjectID, key, retention.SourceKey{Table: "novels", Primary: map[string]string{"id": id}}, retention.SourceKey{Table: "novel_settings_versions", Primary: map[string]string{"novel_id": id, "revision": strconv.FormatInt(book.SettingsRevision, 10)}}); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'novel.settings.confirm',?,?)", a.ID, in.OperationID, hash, id); err != nil {
		return book, err
	}
	if err = audit(ctx, tx, a, "novel.settings.confirm", id); err != nil {
		return book, err
	}
	return book, tx.Commit()
}
func (s *NovelEntry) SettingsVersions(ctx context.Context, actor, id string) ([]NovelSettingsVersion, error) {
	tx, _, _, err := s.settingsTx(ctx, actor, id, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT revision,payload,content_hash FROM novel_settings_versions WHERE novel_id=? ORDER BY revision DESC LIMIT 100", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []NovelSettingsVersion{}
	for rows.Next() {
		var item NovelSettingsVersion
		var raw, hash string
		if err = rows.Scan(&item.Revision, &raw, &hash); err != nil {
			return nil, err
		}
		if digest(raw) != hash || json.Unmarshal([]byte(raw), &item.Setup) != nil {
			return nil, domain.ErrNotReady
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
