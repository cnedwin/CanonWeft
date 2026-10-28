package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

type NovelEntry struct {
	Workspace    *Workspace
	assistanceIP *IP
}
type NovelSetupDraft struct {
	assistantID      string
	ID               string      `json:"id"`
	Revision         int64       `json:"revision"`
	Step             int         `json:"step"`
	Setup            novel.Setup `json:"setup"`
	ConfirmedNovelID string      `json:"confirmed_novel_id"`
}
type NovelBook struct {
	ID               string      `json:"id"`
	ProjectID        string      `json:"project_id"`
	BranchID         string      `json:"branch_id"`
	SettingsRevision int64       `json:"settings_revision"`
	Stage            string      `json:"stage"`
	Setup            novel.Setup `json:"setup"`
}
type NovelConfirmation struct {
	Revision       int64  `json:"revision"`
	OperationID    string `json:"operation_id"`
	AcceptSettings bool   `json:"accept_settings"`
}

type NovelModelChoice struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

func (s *NovelEntry) ModelChoices(ctx context.Context, actor string) ([]NovelModelChoice, error) {
	tx, a, err := s.begin(ctx, actor, true)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT c.id,v.config FROM model_connections c JOIN model_connection_versions v ON v.connection_id=c.id AND v.revision=c.revision WHERE c.workspace_id=? AND v.credential_ref<>''", a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []NovelModelChoice{}
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var config domain.ModelConfig
		if json.Unmarshal(raw, &config) != nil {
			return nil, domain.ErrNotReady
		}
		if config.Enabled {
			result = append(result, NovelModelChoice{ID: id, Name: config.Name, Model: config.Model})
		}
	}
	return result, rows.Err()
}

func (s *NovelEntry) Books(ctx context.Context, actor string) ([]NovelBook, error) {
	tx, a, err := s.begin(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT n.id FROM novels n JOIN projects p ON p.id=n.project_id LEFT JOIN project_members m ON m.project_id=p.id AND m.user_id=? WHERE p.workspace_id=? AND p.deleted_at IS NULL AND (?='owner' OR m.user_id IS NOT NULL) ORDER BY n.created_at DESC,n.id LIMIT 100", a.ID, a.WorkspaceID, a.Role)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []NovelBook{}
	for _, id := range ids {
		book, e := readNovelTx(ctx, tx, a, id)
		if errors.Is(e, domain.ErrForbidden) {
			continue
		}
		if e != nil {
			return nil, e
		}
		result = append(result, book)
	}
	return result, nil
}

func (s *NovelEntry) begin(ctx context.Context, actor string, write bool) (*sql.Tx, domain.Actor, error) {
	tx, err := s.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, domain.Actor{}, err
	}
	a, err := s.Workspace.actorTx(ctx, tx, actor)
	if err == nil && write && !domain.CanEdit(a.Role) {
		err = domain.ErrForbidden
	}
	if err != nil {
		tx.Rollback()
		return nil, a, err
	}
	return tx, a, nil
}

func readNovelDraftTx(ctx context.Context, tx *sql.Tx, a domain.Actor, id string) (NovelSetupDraft, error) {
	d := NovelSetupDraft{ID: id}
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT revision,step,payload,content_hash,confirmed_novel_id FROM novel_setup_drafts WHERE id=? AND actor_id=? AND workspace_id=? FOR UPDATE", id, a.ID, a.WorkspaceID).Scan(&d.Revision, &d.Step, &raw, &hash, &d.ConfirmedNovelID)
	if err == sql.ErrNoRows {
		return d, domain.ErrForbidden
	}
	if err != nil {
		return d, err
	}
	if digest(string(raw)) != hash || json.Unmarshal(raw, &d.Setup) != nil {
		return d, domain.ErrNotReady
	}
	return d, nil
}

func (s *NovelEntry) ReadDraft(ctx context.Context, actor, id string) (NovelSetupDraft, error) {
	tx, a, err := s.begin(ctx, actor, false)
	if err != nil {
		return NovelSetupDraft{}, err
	}
	defer tx.Rollback()
	return readNovelDraftTx(ctx, tx, a, id)
}

func (s *NovelEntry) Drafts(ctx context.Context, actor string) ([]NovelSetupDraft, error) {
	tx, a, err := s.begin(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id FROM novel_setup_drafts WHERE actor_id=? AND workspace_id=? AND confirmed_novel_id='' ORDER BY updated_at DESC,id LIMIT 100", a.ID, a.WorkspaceID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []NovelSetupDraft{}
	for _, id := range ids {
		d, e := readNovelDraftTx(ctx, tx, a, id)
		if e != nil {
			return nil, e
		}
		result = append(result, d)
	}
	return result, nil
}

// SaveDraft uses a caller-created random ID and a compare-and-swap revision.
// Retrying an acknowledged-or-lost save returns its existing revision unchanged.
func (s *NovelEntry) SaveDraft(ctx context.Context, actor string, d NovelSetupDraft) (NovelSetupDraft, error) {
	assistantID := d.assistantID
	d.assistantID = ""
	if !novel.ValidID(d.ID) || d.Revision < 0 || d.Step < 1 || d.Step > 5 || d.ConfirmedNovelID != "" {
		return d, domain.ErrValidation
	}
	if err := d.Setup.Validate(false); err != nil {
		return d, err
	}
	raw, err := json.Marshal(d.Setup)
	if err != nil {
		return d, err
	}
	if len(raw) > 1024*1024 {
		return d, domain.ErrValidation
	}
	hash := digest(string(raw))
	tx, a, err := s.begin(ctx, actor, true)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	var current int64
	if assistantID != "" {
		var project string
		if err = tx.QueryRowContext(ctx, "SELECT project_id FROM generation_tasks WHERE id=? AND actor_id=?", assistantID, a.ID).Scan(&project); err != nil {
			return d, domain.ErrForbidden
		}
		if _, err = projectTx(ctx, tx, a, project); err != nil {
			return d, err
		}
	}
	var oldHash, creator, workspace, confirmed string
	var step int
	err = tx.QueryRowContext(ctx, "SELECT revision,content_hash,actor_id,workspace_id,confirmed_novel_id,step FROM novel_setup_drafts WHERE id=? FOR UPDATE", d.ID).Scan(&current, &oldHash, &creator, &workspace, &confirmed, &step)
	if err != nil && err != sql.ErrNoRows {
		return d, err
	}
	exists := err == nil
	if exists && (creator != a.ID || workspace != a.WorkspaceID) {
		return d, domain.ErrForbidden
	}
	if confirmed != "" {
		return d, domain.ErrConflict
	}
	if exists && current == d.Revision+1 && oldHash == hash && step == d.Step {
		d.Revision = current
		return d, nil
	}
	if current != d.Revision {
		return d, domain.ErrConflict
	}
	if assistantID != "" {
		if err = validateSetupAdoptionTx(ctx, tx, a, d.ID, assistantID, s.assistanceIP); err != nil {
			return d, err
		}
	}
	d.Revision++
	if exists {
		_, err = tx.ExecContext(ctx, "UPDATE novel_setup_drafts SET revision=?,step=?,payload=?,content_hash=?,updated_at=CURRENT_TIMESTAMP(6) WHERE id=?", d.Revision, d.Step, raw, hash, d.ID)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO novel_setup_drafts(id,workspace_id,actor_id,revision,step,payload,content_hash) VALUES(?,?,?,?,?,?,?)", d.ID, a.WorkspaceID, a.ID, d.Revision, d.Step, raw, hash)
	}
	if err != nil {
		return d, err
	}
	if err = audit(ctx, tx, a, "novel.setup.save", d.ID); err != nil {
		return d, err
	}
	return d, tx.Commit()
}

func readNovelTx(ctx context.Context, tx *sql.Tx, a domain.Actor, id string) (NovelBook, error) {
	var book NovelBook
	var raw []byte
	var hash string
	err := tx.QueryRowContext(ctx, "SELECT n.id,n.project_id,n.branch_id,n.settings_revision,n.stage,v.payload,v.content_hash FROM novels n JOIN novel_settings_versions v ON v.novel_id=n.id AND v.revision=n.settings_revision WHERE n.id=?", id).Scan(&book.ID, &book.ProjectID, &book.BranchID, &book.SettingsRevision, &book.Stage, &raw, &hash)
	if err == sql.ErrNoRows {
		return book, domain.ErrForbidden
	}
	if err != nil {
		return book, err
	}
	if _, err = projectTx(ctx, tx, a, book.ProjectID); err != nil {
		return book, err
	}
	if digest(string(raw)) != hash || json.Unmarshal(raw, &book.Setup) != nil {
		return book, domain.ErrNotReady
	}
	return book, nil
}

func (s *NovelEntry) Book(ctx context.Context, actor, id string) (NovelBook, error) {
	tx, a, err := s.begin(ctx, actor, false)
	if err != nil {
		return NovelBook{}, err
	}
	defer tx.Rollback()
	return readNovelTx(ctx, tx, a, id)
}

// Confirm commits the book, settings, branch and private character candidates in
// one transaction. No model, workflow, canonical publication or payment is started.
func (s *NovelEntry) Confirm(ctx context.Context, actor, id string, input NovelConfirmation) (NovelBook, error) {
	var book NovelBook
	if !input.AcceptSettings || input.Revision < 1 || !validName(input.OperationID, 120) {
		return book, domain.ErrValidation
	}
	tx, a, err := s.begin(ctx, actor, true)
	if err != nil {
		return book, err
	}
	defer tx.Rollback()
	if _, _, err = requestIdentityPolicyTx(ctx, tx, a.WorkspaceID); err != nil {
		return book, err
	}
	d, err := readNovelDraftTx(ctx, tx, a, id)
	if err != nil {
		return book, err
	}
	encoded, _ := json.Marshal(input)
	hash := digest("novel.create\x00" + id + "\x00" + string(encoded))
	stored, result, err := operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if stored != hash {
			return book, domain.ErrConflict
		}
		return readNovelTx(ctx, tx, a, result)
	}
	if err != sql.ErrNoRows {
		return book, err
	}
	if d.Revision != input.Revision || d.ConfirmedNovelID != "" {
		return book, domain.ErrConflict
	}
	if err = d.Setup.Validate(true); err != nil {
		return book, err
	}
	if d.Setup.ModelChoice == "configured" {
		var count int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM model_connections c JOIN model_connection_versions v ON v.connection_id=c.id AND v.revision=c.revision WHERE c.id=? AND c.workspace_id=? AND v.credential_ref<>'' AND JSON_EXTRACT(v.config,'$.enabled')=TRUE", d.Setup.ConnectionID, a.WorkspaceID).Scan(&count)
		if err != nil {
			return book, err
		}
		if count != 1 {
			return book, domain.ErrValidation
		}
	}
	project := d.Setup.ProjectID
	prepared, preparedErr := preparedSetupWorkspaceTx(ctx, tx, a, d)
	if preparedErr != nil && preparedErr != sql.ErrNoRows {
		return book, preparedErr
	}
	if preparedErr == nil && prepared.Branch.Head != d.Setup.BaseHead {
		return book, domain.ErrConflict
	}
	base := domain.Snapshot{Entities: []domain.Entity{}, Assertions: []domain.Assertion{}}
	if d.Setup.IPMode == "existing" {
		p, e := projectTx(ctx, tx, a, project)
		if e != nil {
			return book, e
		}
		if !domain.CanEdit(p.Role) {
			return book, domain.ErrForbidden
		}
		if _, e = branchTx(ctx, tx, project, d.Setup.BranchID); e != nil {
			return book, e
		}
		var count int
		if e = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM ip_revisions WHERE id=? AND branch_id=? AND status='PUBLISHED'", d.Setup.BaseHead, d.Setup.BranchID).Scan(&count); e != nil {
			return book, e
		}
		if count != 1 {
			return book, domain.ErrConflict
		}
		base, e = snapshotTx(ctx, tx, project, d.Setup.BaseHead)
		if e != nil {
			return book, e
		}
	} else {
		if preparedErr == nil {
			project = prepared.Project.ID
			if _, err = tx.ExecContext(ctx, "UPDATE projects SET name=?,revision=revision+1 WHERE id=?", strings.TrimSpace(d.Setup.Title), project); err != nil {
				return book, err
			}
			if err = captureMetadataSourceTx(ctx, tx, project, "projects", project); err != nil {
				return book, err
			}
		} else {
			project = NewID()
			if _, err = tx.ExecContext(ctx, "INSERT INTO projects(id,workspace_id,name) VALUES(?,?,?)", project, a.WorkspaceID, strings.TrimSpace(d.Setup.Title)); err != nil {
				return book, err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,'owner')", project, a.ID); err != nil {
				return book, err
			}
			if err = protectedLedger().AppendTx(ctx, tx, authority.Event{Kind: authority.ProjectCreated, ProjectID: project, ResourceID: project, ActorID: a.ID}); err != nil {
				return book, err
			}
			if err = captureMetadataSourceTx(ctx, tx, project, "projects", project); err != nil {
				return book, err
			}
		}
	}
	book = NovelBook{ID: NewID(), ProjectID: project, BranchID: NewID(), SettingsRevision: 1, Stage: "WORLD_PLANNING", Setup: d.Setup}
	if preparedErr == nil {
		book.BranchID = prepared.Branch.ID
	}
	branchName := "小说 · " + strings.TrimSpace(d.Setup.Title)
	// A branch title is presentation only; reserve room for its disambiguating ID.
	runes := []rune(branchName)
	if len(runes) > 95 {
		branchName = string(runes[:95])
	}
	branchName += " · " + book.ID[:12]
	if preparedErr == nil {
		_, err = tx.ExecContext(ctx, "UPDATE branches SET name=?,revision=revision+1 WHERE id=? AND project_id=?", branchName, book.BranchID, project)
	} else {
		_, err = tx.ExecContext(ctx, "INSERT INTO branches(id,project_id,name,parent_id,base_revision,head_revision) VALUES(?,?,?,?,?,?)", book.BranchID, project, branchName, d.Setup.BranchID, d.Setup.BaseHead, d.Setup.BaseHead)
	}
	if err != nil {
		return book, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "branches", book.BranchID); err != nil {
		return book, err
	}
	initial := d.Setup.InitialSnapshot(NewID, time.Now().UTC().Format(time.RFC3339Nano))
	base.Entities = append(base.Entities, initial.Entities...)
	base.Assertions = append(base.Assertions, initial.Assertions...)
	ipPayload, err := json.Marshal(domain.StoredIPDraft{DraftSchema: 1, Snapshot: base})
	if err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO ip_drafts(branch_id,actor_id,revision,base_head,payload) VALUES(?,?,1,?,?)", book.BranchID, a.ID, d.Setup.BaseHead, ipPayload); err != nil {
		return book, err
	}
	ip := &IP{Workspace: s.Workspace}
	if err = ip.captureSourceValuesTx(ctx, tx, project, retention.SourceKey{Table: "ip_drafts", Primary: map[string]string{"branch_id": book.BranchID, "actor_id": a.ID}}); err != nil {
		return book, err
	}
	settings, _ := json.Marshal(d.Setup)
	if _, err = tx.ExecContext(ctx, "INSERT INTO novels(id,project_id,branch_id,created_by,setup_id,settings_revision,stage) VALUES(?,?,?,?,?,1,?)", book.ID, project, book.BranchID, a.ID, id, book.Stage); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO novel_settings_versions(novel_id,revision,payload,content_hash,created_by) VALUES(?,1,?,?,?)", book.ID, settings, digest(string(settings)), a.ID); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE novel_setup_drafts SET confirmed_novel_id=? WHERE id=?", book.ID, id); err != nil {
		return book, err
	}
	if err = ip.captureSourceValuesTx(ctx, tx, project,
		retention.SourceKey{Table: "novel_setup_drafts", Primary: map[string]string{"id": id}},
		retention.SourceKey{Table: "novels", Primary: map[string]string{"id": book.ID}},
		retention.SourceKey{Table: "novel_settings_versions", Primary: map[string]string{"novel_id": book.ID, "revision": "1"}},
	); err != nil {
		return book, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'novel.create',?,?)", a.ID, input.OperationID, hash, book.ID); err != nil {
		return book, err
	}
	if err = audit(ctx, tx, a, "novel.create", book.ID); err != nil {
		return book, err
	}
	return book, tx.Commit()
}
