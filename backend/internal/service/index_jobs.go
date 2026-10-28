package service

import (
	"context"
	"database/sql"
	"errors"
	"novelstudio/internal/domain"
	"slices"
)

type IndexJobs struct {
	Knowledge *Knowledge
	Heartbeat func(context.Context)
}
type IndexJobInput struct {
	Kind        string `json:"kind,omitempty"`
	OperationID string `json:"operation_id"`
	ReleaseID   string `json:"release_id,omitempty"`
	CandidateID string `json:"candidate_id,omitempty"`
}
type IndexJob struct {
	Kind         string `json:"kind"`
	Stale        bool   `json:"stale"`
	CanCancel    bool   `json:"can_cancel"`
	ID           string `json:"id"`
	ProjectID    string `json:"project_id"`
	BranchID     string `json:"branch_id"`
	ActorID      string `json:"-"`
	IndexID      string `json:"index_id"`
	ReleaseID    string `json:"release_id"`
	CandidateID  string `json:"candidate_id"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	TotalSources int    `json:"total_sources"`
	DoneSources  int    `json:"done_sources"`
	Chunks       int    `json:"chunks"`
}

const indexJobColumns = "id,project_id,branch_id,actor_id,index_id,release_id,candidate_id,state,reason,total_sources,done_sources,chunks,kind"

func scanIndexJob(row interface{ Scan(...any) error }) (IndexJob, error) {
	var j IndexJob
	err := row.Scan(&j.ID, &j.ProjectID, &j.BranchID, &j.ActorID, &j.IndexID, &j.ReleaseID, &j.CandidateID, &j.State, &j.Reason, &j.TotalSources, &j.DoneSources, &j.Chunks, &j.Kind)
	if err == sql.ErrNoRows {
		err = domain.ErrForbidden
	}
	return j, err
}
func indexJobTx(ctx context.Context, tx *sql.Tx, project, id string) (IndexJob, error) {
	return scanIndexJob(tx.QueryRowContext(ctx, "SELECT "+indexJobColumns+" FROM index_jobs WHERE project_id=? AND id=? FOR UPDATE", project, id))
}
func (j IndexJob) prospective() bool { return j.ReleaseID != "" || j.CandidateID != "" }
func (s *IndexJobs) resolve(ctx context.Context, tx *sql.Tx, j IndexJob) (IndexInfo, error) {
	var info IndexInfo
	var err error
	if j.Kind == "working" {
		private, _, err := s.Knowledge.privateManifest(ctx, tx, j.ActorID, j.ProjectID, j.BranchID)
		if err == nil && j.IndexID != "" && private.ID != j.IndexID {
			err = domain.ErrConflict
		}
		info.ID = private.ID
		info.Manifest.Sources = make([]SourcePin, len(private.Manifest.Sources))
		return info, err
	}
	if j.prospective() {
		info, err = s.Knowledge.IP.publicationInfo(ctx, tx, j.ProjectID, j.BranchID, j.ReleaseID, j.CandidateID)
	} else {
		info, err = knowledgeManifest(ctx, tx, j.ProjectID, j.BranchID)
	}
	if errors.Is(err, errAlreadyPublished) {
		info, err = knowledgeManifest(ctx, tx, j.ProjectID, j.BranchID)
	}
	if err != nil {
		return info, err
	}
	if j.IndexID != "" && info.ID != j.IndexID {
		return info, domain.ErrConflict
	}
	return info, nil
}
func (s *IndexJobs) Create(ctx context.Context, a domain.Actor, project, branch string, input IndexJobInput) (IndexJob, error) {
	var job IndexJob
	if input.Kind == "published" {
		input.Kind = ""
	} // preserve historical operation fingerprints
	if input.Kind != "" && input.Kind != "working" || input.Kind == "working" && (input.ReleaseID != "" || input.CandidateID != "") {
		return job, domain.ErrValidation
	}
	if !validName(input.OperationID, 120) || (input.ReleaseID != "" && input.CandidateID != "") {
		return job, domain.ErrValidation
	}
	job = IndexJob{ProjectID: project, BranchID: branch, ReleaseID: input.ReleaseID, CandidateID: input.CandidateID, Kind: input.Kind}
	tx, a, err := s.Knowledge.beginIndex(ctx, a, project, job.prospective())
	if err != nil {
		return job, err
	}
	defer tx.Rollback()
	job.ActorID = a.ID
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return job, err
	}
	requestHash := valueHash(struct {
		Kind, Project, Branch string
		Input                 IndexJobInput
	}{"index.job", project, branch, input})
	var previous, id string
	previous, id, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if previous != requestHash {
			return job, domain.ErrConflict
		}
		return indexJobTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return job, err
	}
	if input.ReleaseID != "" {
		r, err := releaseTx(ctx, tx, project, input.ReleaseID)
		if err != nil {
			return job, err
		}
		if r.Manifest.BranchID != branch {
			return job, domain.ErrForbidden
		}
		if r.Status != "READY" {
			return job, domain.ErrConflict
		}
	}
	if input.CandidateID != "" {
		c, err := candidateTx(ctx, tx, branch, input.CandidateID)
		if err != nil {
			return job, err
		}
		if c.Status != "READY" {
			return job, domain.ErrConflict
		}
	}
	info, err := s.resolve(ctx, tx, job)
	if err != nil {
		return job, err
	}
	job.ID = NewID()
	job.ActorID = a.ID
	job.IndexID = info.ID
	job.TotalSources = len(info.Manifest.Sources)
	job.State = "QUEUED"
	job.CanCancel = true
	if job.Kind == "" {
		job.Kind = "published"
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO index_jobs(id,project_id,branch_id,actor_id,index_id,release_id,candidate_id,total_sources,kind) VALUES(?,?,?,?,?,?,?,?,?)", job.ID, project, branch, a.ID, info.ID, job.ReleaseID, job.CandidateID, job.TotalSources, job.Kind); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'index.job',?,?)", a.ID, input.OperationID, requestHash, job.ID); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'index.start',?,?)", NewID(), project, job.ID, `{}`); err != nil {
		return job, err
	}
	if err = audit(ctx, tx, a, "index.job", job.ID); err != nil {
		return job, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "index_jobs", job.ID); err != nil {
		return job, err
	}
	return job, tx.Commit()
}
func (s *IndexJobs) Read(ctx context.Context, a domain.Actor, project, branch, id string) (IndexJob, error) {
	tx, a, p, err := s.Knowledge.IP.begin(ctx, a, project, false)
	if err != nil {
		return IndexJob{}, err
	}
	defer tx.Rollback()
	job, err := indexJobTx(ctx, tx, project, id)
	if err == nil && (job.BranchID != branch || job.Kind == "working" && job.ActorID != a.ID) {
		err = domain.ErrForbidden
	}
	if err == nil {
		err = s.decorate(ctx, tx, a, p.Role, &job)
	}
	return job, err
}
func (s *IndexJobs) List(ctx context.Context, a domain.Actor, project, branch string) ([]IndexJob, error) {
	tx, a, p, err := s.Knowledge.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+indexJobColumns+" FROM index_jobs WHERE project_id=? AND branch_id=? AND (kind='published' OR actor_id=?) ORDER BY created_at DESC,id DESC LIMIT 100", project, branch, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []IndexJob{}
	for rows.Next() {
		job, err := scanIndexJob(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range items {
		if err = s.decorate(ctx, tx, a, p.Role, &items[i]); err != nil {
			return nil, err
		}
	}
	return items, nil
}
func (s *IndexJobs) decorate(ctx context.Context, tx *sql.Tx, a domain.Actor, role string, j *IndexJob) error {
	j.CanCancel = a.Role != "reader" && role != "reader" && (a.ID == j.ActorID || a.Role == "owner") && slices.Contains([]string{"QUEUED", "RUNNING"}, j.State)
	_, err := s.resolve(ctx, tx, *j)
	if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrNotReady) {
		j.Stale = true
		return nil
	}
	return err
}
func (s *IndexJobs) Cancel(ctx context.Context, a domain.Actor, project, branch, id string) (IndexJob, error) {
	tx, a, p, err := s.Knowledge.IP.begin(ctx, a, project, false)
	if err != nil {
		return IndexJob{}, err
	}
	defer tx.Rollback()
	job, err := indexJobTx(ctx, tx, project, id)
	if err != nil {
		return job, err
	}
	if job.BranchID != branch || job.Kind == "working" && job.ActorID != a.ID || a.Role == "reader" || p.Role == "reader" || (a.ID != job.ActorID && a.Role != "owner") {
		return job, domain.ErrForbidden
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING"}, job.State) {
		return job, nil
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "index_jobs", id); err != nil {
		return job, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE index_jobs SET state='CANCELLED',reason='已停止后续索引处理' WHERE id=?", id); err != nil {
		return job, err
	}
	if err = audit(ctx, tx, a, "index.cancel", id); err != nil {
		return job, err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "index_jobs", id); err != nil {
		return job, err
	}
	job.State = "CANCELLED"
	job.Reason = "已停止后续索引处理"
	return job, tx.Commit()
}
func (s *IndexJobs) finish(ctx context.Context, j IndexJob, state, reason string) (string, error) {
	var project string
	db := s.Knowledge.IP.Workspace.DB
	if err := db.QueryRowContext(ctx, "SELECT project_id FROM index_jobs WHERE id=?", j.ID).Scan(&project); err != nil {
		return "", err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, project); err != nil {
		return "", err
	}
	current, err := indexJobTx(ctx, tx, project, j.ID)
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING"}, current.State) {
		return current.State, nil
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "index_jobs", j.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE index_jobs SET state=?,reason=? WHERE id=?", state, reason, j.ID); err != nil {
		return "", err
	}
	if err = captureMetadataSourceTx(ctx, tx, project, "index_jobs", j.ID); err != nil {
		return "", err
	}
	return state, tx.Commit()
}
func (s *IndexJobs) Fail(ctx context.Context, id string) (string, error) {
	return s.finish(ctx, IndexJob{ID: id}, "FAILED", "后台执行多次未完成；保留旧索引，可重新准备任务")
}
func (s *IndexJobs) Execute(ctx context.Context, id string) (string, error) {
	job, err := scanIndexJob(s.Knowledge.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+indexJobColumns+" FROM index_jobs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING"}, job.State) {
		return job.State, nil
	}
	result, err := s.buildJob(ctx, job)
	if err != nil {
		if ctx.Err() != nil {
			return "RUNNING", ctx.Err()
		}
		state, reason := "FAILED", "本地索引构建失败；旧内容与索引保留，可检查服务后重试"
		if errors.Is(err, domain.ErrConflict) || errors.Is(err, domain.ErrForbidden) || errors.Is(err, domain.ErrUnauthorized) {
			state = "BLOCKED"
			reason = "输入版本或权限已变化，请按最新内容重新准备"
		}
		return s.finish(ctx, job, state, reason)
	}
	tx, _, err := s.Knowledge.beginIndex(ctx, domain.Actor{ID: job.ActorID}, job.ProjectID, job.prospective())
	if err != nil {
		return s.finish(ctx, job, "BLOCKED", "权限已变化，索引结果没有应用到任务")
	}
	defer tx.Rollback()
	current, err := indexJobTx(ctx, tx, job.ProjectID, job.ID)
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"QUEUED", "RUNNING"}, current.State) {
		return current.State, nil
	}
	if _, err = s.resolve(ctx, tx, job); err != nil {
		tx.Rollback()
		return s.finish(ctx, job, "BLOCKED", "输入已变化，请按最新内容重新准备")
	}
	if err = captureMetadataSourceTx(ctx, tx, job.ProjectID, "index_jobs", job.ID); err != nil {
		return "", err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE index_jobs SET state='READY',done_sources=total_sources,chunks=?,reason='' WHERE id=?", result.Chunks, job.ID); err != nil {
		return "", err
	}
	if err = captureMetadataSourceTx(ctx, tx, job.ProjectID, "index_jobs", job.ID); err != nil {
		return "", err
	}
	return "READY", tx.Commit()
}
func (s *IndexJobs) buildJob(ctx context.Context, job IndexJob) (IndexInfo, error) {
	a := domain.Actor{ID: job.ActorID}
	tx, _, err := s.Knowledge.beginIndex(ctx, a, job.ProjectID, job.prospective())
	if err != nil {
		return IndexInfo{}, err
	}
	current, err := indexJobTx(ctx, tx, job.ProjectID, job.ID)
	if err == nil && !slices.Contains([]string{"QUEUED", "RUNNING"}, current.State) {
		err = domain.ErrConflict
	}
	if err == nil {
		_, err = s.resolve(ctx, tx, job)
	}
	if err == nil {
		err = captureMetadataSourceTx(ctx, tx, job.ProjectID, "index_jobs", job.ID)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE index_jobs SET state='RUNNING',reason='' WHERE id=?", job.ID)
	}
	if err == nil {
		err = captureMetadataSourceTx(ctx, tx, job.ProjectID, "index_jobs", job.ID)
	}
	if err != nil {
		tx.Rollback()
		return IndexInfo{}, err
	}
	if err = tx.Commit(); err != nil {
		return IndexInfo{}, err
	}
	knowledge := *s.Knowledge
	knowledge.Progress = func(ctx context.Context, done, chunks int) error {
		tx, _, err := s.Knowledge.beginIndex(ctx, a, job.ProjectID, job.prospective())
		if err != nil {
			return err
		}
		defer tx.Rollback()
		current, err := indexJobTx(ctx, tx, job.ProjectID, job.ID)
		if err != nil {
			return err
		}
		if current.State != "RUNNING" {
			return domain.ErrConflict
		}
		if _, err = tx.ExecContext(ctx, "UPDATE index_jobs SET done_sources=?,chunks=? WHERE id=?", done, chunks, job.ID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if s.Heartbeat != nil {
			s.Heartbeat(ctx)
		}
		return nil
	}
	resolve := func(tx *sql.Tx) (IndexInfo, error) {
		current, err := indexJobTx(ctx, tx, job.ProjectID, job.ID)
		if err != nil {
			return IndexInfo{}, err
		}
		if current.State != "RUNNING" {
			return IndexInfo{}, domain.ErrConflict
		}
		return s.resolve(ctx, tx, job)
	}
	if job.Kind == "working" {
		info, err := knowledge.buildPrivate(ctx, a, job.ProjectID, job.BranchID, func(tx *sql.Tx) error { _, err := resolve(tx); return err })
		return IndexInfo{ID: info.ID, Chunks: info.Chunks}, err
	}
	return knowledge.build(ctx, a, job.ProjectID, job.BranchID, job.prospective(), resolve)
}
