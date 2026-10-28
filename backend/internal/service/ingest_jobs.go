package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retrieval"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Implementations are supplied only by the trusted process, never HTTP input.
type IngestParser interface {
	Plan(context.Context, string, []byte, string) (retrieval.ImportPlan, error)
	Unit(context.Context, string, []byte, retrieval.ImportPlan, int, string) (retrieval.ImportUnit, error)
}
type localIngestParser struct{}

func (localIngestParser) Plan(c context.Context, n string, b []byte, o string) (retrieval.ImportPlan, error) {
	return retrieval.PlanImport(c, n, b, o)
}
func (localIngestParser) Unit(c context.Context, n string, b []byte, p retrieval.ImportPlan, i int, o string) (retrieval.ImportUnit, error) {
	return retrieval.ExtractImportUnit(c, n, b, p, i, o)
}

type IngestJobs struct {
	IP     *IP
	Parser IngestParser
}
type IngestProgress struct {
	Plan  *retrieval.ImportPlan  `json:"plan"`
	Units []retrieval.ImportUnit `json:"units"`
}
type IngestJob struct {
	Retention       *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial []byte
	ID              string         `json:"id"`
	ProjectID       string         `json:"project_id"`
	BranchID        string         `json:"branch_id"`
	ActorID         string         `json:"-"`
	Name            string         `json:"name"`
	Hash            string         `json:"hash"`
	Size            int64          `json:"size"`
	ParserRevision  string         `json:"parser_revision"`
	OCR             string         `json:"ocr"`
	Progress        IngestProgress `json:"progress"`
	State           string         `json:"state"`
	Paused          bool           `json:"paused"`
	Cancelled       bool           `json:"cancelled"`
	Revision        int64          `json:"revision"`
	Reason          string         `json:"reason"`
}

const ingestColumns = "id,project_id,branch_id,actor_id,filename,source_hash,source_size,parser_revision,ocr_mode,progress,progress_hash,state,paused,cancel_requested,revision,reason"

func scanIngest(row interface{ Scan(...any) error }) (IngestJob, error) {
	var j IngestJob
	var raw []byte
	var hash string
	err := row.Scan(&j.ID, &j.ProjectID, &j.BranchID, &j.ActorID, &j.Name, &j.Hash, &j.Size, &j.ParserRevision, &j.OCR, &raw, &hash, &j.State, &j.Paused, &j.Cancelled, &j.Revision, &j.Reason)
	if err == sql.ErrNoRows {
		return j, domain.ErrForbidden
	}
	if err != nil {
		return j, err
	}
	if err := rejectExpiredRuntime(raw, "ingest_jobs.progress"); err != nil {
		j.expiredMaterial = raw
		return j, err
	}
	if digest(string(raw)) != hash || json.Unmarshal(raw, &j.Progress) != nil || j.Revision < 1 {
		return j, domain.ErrNotReady
	}
	return j, nil
}
func ingestTx(ctx context.Context, tx *sql.Tx, project, id string, history ...bool) (IngestJob, error) {
	job, err := scanIngest(tx.QueryRowContext(ctx, "SELECT "+ingestColumns+" FROM ingest_jobs WHERE project_id=? AND id=? FOR UPDATE", project, id))
	if errors.Is(err, domain.ErrMaterialExpired) && len(history) > 0 && history[0] {
		job.Retention, err = runtimeTransientTx(ctx, tx, project, "ingest_jobs", "progress", id)
	}
	return job, err
}
func (j IngestJob) terminal() bool {
	return slices.Contains([]string{"COMPLETED", "FAILED", "CANCELLED"}, j.State)
}
func (j IngestJob) displayState() string {
	if !j.terminal() && j.Paused {
		return "PAUSED"
	}
	return j.State
}
func (j IngestJob) owns(a domain.Actor, branch string) bool {
	return j.ActorID == a.ID && j.BranchID == branch
}

func (s *IngestJobs) Create(ctx context.Context, a domain.Actor, project, branch, name, op string, data []byte) (IngestJob, error) {
	var j IngestJob
	if !validName(name, 160) || !validName(op, 120) || strings.ContainsAny(name, "/\\\x00") || len(data) == 0 || len(data) > 32*1024*1024 || !slices.Contains([]string{".txt", ".md", ".docx", ".pdf", ".png", ".jpg", ".jpeg", ".webp"}, strings.ToLower(filepath.Ext(name))) {
		return j, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return j, err
	}
	_, err = branchTx(ctx, tx, project, branch)
	tx.Rollback()
	if err != nil {
		return j, err
	}
	hash := digest(string(data))
	requestHash := valueHash([]string{"ingest-v1", project, branch, name, hash})
	tx, a, _, err = s.IP.begin(ctx, a, project, true)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return j, err
	}
	var id, prior string
	id, prior, err = requestReceiptTx(ctx, tx, a, "ingest_jobs", op, false)
	if err == nil {
		if prior != requestHash {
			return j, domain.ErrConflict
		}
		return ingestTx(ctx, tx, project, id)
	}
	if err != sql.ErrNoRows {
		return j, err
	}
	object, err := objectwriter.Write(ctx, tx, s.IP.Blobs, project, data)
	if err != nil {
		return j, err
	}
	parser := "bounded-native-v1"
	if retrieval.DocumentImport(name) {
		parser = retrieval.DocumentParser
	}
	ocr := "enabled"
	if os.Getenv("STUDIO_INGEST_OCR") == "disabled" {
		ocr = "disabled"
	}
	j = IngestJob{ID: NewID(), ProjectID: project, BranchID: branch, ActorID: a.ID, Name: name, Hash: hash, Size: object.Size, ParserRevision: parser, OCR: ocr, Progress: IngestProgress{Units: []retrieval.ImportUnit{}}, State: "QUEUED", Revision: 1}
	raw, _ := json.Marshal(j.Progress)
	_, err = tx.ExecContext(ctx, "INSERT INTO ingest_jobs(id,project_id,branch_id,actor_id,operation_id,request_hash,filename,source_hash,source_size,parser_revision,ocr_mode,progress,progress_hash) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)", j.ID, project, branch, a.ID, op, requestHash, name, hash, object.Size, parser, ocr, raw, digest(string(raw)))
	if err != nil {
		return j, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'ingest.start',?,'{}')", NewID(), project, j.ID); err != nil {
		return j, err
	}
	if err = audit(ctx, tx, a, "source.ingest", j.ID); err != nil {
		return j, err
	}
	if err = s.IP.captureImportSourceTx(ctx, tx, project, "ingest_jobs", j.ID); err != nil {
		return j, err
	}
	return j, tx.Commit()
}
func (s *IngestJobs) Read(ctx context.Context, a domain.Actor, project, branch, id string) (IngestJob, error) {
	var j IngestJob
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	j, err = ingestTx(ctx, tx, project, id, true)
	if err == nil && !j.owns(a, branch) {
		err = domain.ErrForbidden
	}
	return j, err
}
func (s *IngestJobs) List(ctx context.Context, a domain.Actor, project, branch string) ([]IngestJob, error) {
	tx, a, _, err := s.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+ingestColumns+" FROM ingest_jobs WHERE project_id=? AND branch_id=? AND actor_id=? ORDER BY created_at DESC,id DESC LIMIT 100", project, branch, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	jobs := []IngestJob{}
	for rows.Next() {
		j, e := scanIngest(rows)
		if e != nil && !errors.Is(e, domain.ErrMaterialExpired) {
			return nil, e
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for i := range jobs {
		if jobs[i].expiredMaterial != nil {
			jobs[i].Retention, err = runtimeTransientTx(ctx, tx, project, "ingest_jobs", "progress", jobs[i].ID)
			if err != nil {
				return nil, err
			}
		}
	}
	return jobs, nil
}
func (s *IngestJobs) Control(ctx context.Context, a domain.Actor, project, branch, id, action string, revision int64) (IngestJob, error) {
	var j IngestJob
	if !slices.Contains([]string{"pause", "resume", "cancel"}, action) {
		return j, domain.ErrValidation
	}
	tx, a, _, err := s.IP.begin(ctx, a, project, true)
	if err != nil {
		return j, err
	}
	defer tx.Rollback()
	j, err = ingestTx(ctx, tx, project, id)
	if err != nil {
		return j, err
	}
	if !j.owns(a, branch) {
		return j, domain.ErrForbidden
	}
	if j.Revision != revision || j.terminal() {
		return j, domain.ErrConflict
	}
	if action == "cancel" {
		j.Cancelled = true
		j.State = "CANCELLED"
	} else {
		j.Paused = action == "pause"
	}
	j.Revision++
	if _, err = tx.ExecContext(ctx, "UPDATE ingest_jobs SET state=?,paused=?,cancel_requested=?,revision=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", j.State, j.Paused, j.Cancelled, j.Revision, id); err != nil {
		return j, err
	}
	if err = audit(ctx, tx, a, "source.ingest."+action, id); err != nil {
		return j, err
	}
	return j, tx.Commit()
}

func (s *IngestJobs) guard(ctx context.Context, j IngestJob) (*sql.Tx, IngestJob, error) {
	tx, _, _, err := s.IP.begin(ctx, domain.Actor{ID: j.ActorID}, j.ProjectID, true)
	if err != nil {
		return nil, j, err
	}
	latest, err := ingestTx(ctx, tx, j.ProjectID, j.ID)
	if err != nil {
		tx.Rollback()
		return nil, j, err
	}
	return tx, latest, nil
}
func (s *IngestJobs) permissionError(ctx context.Context, j IngestJob, err error) (string, error) {
	if !errors.Is(err, domain.ErrForbidden) && !errors.Is(err, domain.ErrUnauthorized) {
		return "", err
	}
	_, err = s.IP.Workspace.DB.ExecContext(ctx, "UPDATE ingest_jobs SET state='CANCELLED',cancel_requested=TRUE,revision=revision+1,reason='ACCESS_REVOKED',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND state IN ('QUEUED','RUNNING')", j.ID)
	return "CANCELLED", err
}

// Each call checkpoints at most one expensive page. Temporal can replay calls
// after a lost receipt; CAS prevents late pages from crossing control boundaries.
func (s *IngestJobs) Advance(ctx context.Context, id string) (string, error) {
	j, err := scanIngest(s.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+ingestColumns+" FROM ingest_jobs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	tx, j, err := s.guard(ctx, j)
	if err != nil {
		return s.permissionError(ctx, j, err)
	}
	if j.terminal() || j.Paused {
		tx.Rollback()
		return j.displayState(), nil
	}
	tx.Rollback()
	data, err := s.IP.Blobs.ReadObject(ctx, j.ProjectID, j.Hash)
	if err != nil {
		return "", err
	}
	if int64(len(data)) != j.Size || digest(string(data)) != j.Hash {
		return "", domain.ErrNotReady
	}
	parser := s.Parser
	if parser == nil {
		parser = localIngestParser{}
	}
	progress := j.Progress
	reason := ""
	if progress.Plan == nil {
		plan, e := parser.Plan(ctx, j.Name, data, j.OCR)
		err = e
		if err == nil {
			if plan.Parser != j.ParserRevision || plan.Count < 1 || plan.Count > 10000 {
				return "", domain.ErrNotReady
			}
			progress.Plan = &plan
			if plan.Native != nil {
				progress.Units = plan.Native.Units
				plan.Native = nil
			}
		}
	} else if len(progress.Units) < progress.Plan.Count {
		unit, e := parser.Unit(ctx, j.Name, data, *progress.Plan, len(progress.Units), j.OCR)
		err = e
		if err == nil {
			progress.Units = append(progress.Units, retrieval.LimitImportUnit(progress.Units, unit))
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		var parserError *retrieval.DocumentError
		if errors.As(err, &parserError) && (parserError.Code == "busy" || parserError.Code == "runtime_unavailable") {
			return "", err
		}
		if !errors.Is(err, retrieval.ErrImport) {
			return "", err
		}
		reason = "IMPORT_REJECTED"
		if parserError != nil && slices.Contains([]string{"encrypted", "corrupt", "input_limit", "page_limit", "pixel_limit", "animated_image"}, parserError.Code) {
			reason = parserError.Code
		}
	}
	tx, current, e := s.guard(ctx, j)
	if e != nil {
		return s.permissionError(ctx, j, e)
	}
	defer tx.Rollback()
	if current.terminal() || current.Paused {
		return current.displayState(), nil
	}
	if current.Revision != j.Revision {
		return current.displayState(), nil
	}
	if e = s.IP.captureImportSourceTx(ctx, tx, j.ProjectID, "ingest_jobs", j.ID); e != nil {
		return "", e
	}
	state := "RUNNING"
	if reason != "" {
		state = "FAILED"
		progress = j.Progress
	}
	var report retrieval.ImportReport
	if state != "FAILED" && progress.Plan != nil && len(progress.Units) == progress.Plan.Count {
		report, e = retrieval.CompleteImportReport(progress.Plan.Parser, progress.Units)
		if e != nil {
			return "", e
		}
		state = "COMPLETED"
	}
	raw, _ := json.Marshal(progress)
	if len(raw) > 2*1024*1024 {
		return "", domain.ErrValidation
	}
	if state == "COMPLETED" {
		reportRaw, _ := json.Marshal(report)
		fingerprint := digest(j.ProjectID + "\x00" + j.BranchID + "\x00" + j.Name + "\x00" + j.Hash)
		if _, e = tx.ExecContext(ctx, "INSERT INTO source_imports(id,project_id,branch_id,actor_id,filename,source_hash,source_size,report,request_hash,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?)", j.ID, j.ProjectID, j.BranchID, j.ActorID, j.Name, j.Hash, j.Size, reportRaw, fingerprint, "ingest-"+j.ID); e != nil {
			return "", e
		}
		if e = s.IP.captureImportSourceTx(ctx, tx, j.ProjectID, "source_imports", j.ID); e != nil {
			return "", e
		}
	}
	_, e = tx.ExecContext(ctx, "UPDATE ingest_jobs SET progress=?,progress_hash=?,state=?,reason=?,revision=revision+1,updated_at=UTC_TIMESTAMP(6) WHERE id=?", raw, digest(string(raw)), state, reason, j.ID)
	if e != nil {
		return "", e
	}
	if e = s.IP.captureImportSourceTx(ctx, tx, j.ProjectID, "ingest_jobs", j.ID); e != nil {
		return "", e
	}
	return state, tx.Commit()
}
