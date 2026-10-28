package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// RetentionJobReport exposes only historical plan counts. CheckpointCurrent is
// a database comparison, not proof of present file state or deletion authority.
type RetentionJobReport struct {
	Automatic         bool      `json:"automatic"`
	ID                string    `json:"id"`
	ProjectID         string    `json:"project_id"`
	State             string    `json:"state"`
	Reason            string    `json:"reason,omitempty"`
	PlanHash          string    `json:"plan_hash"`
	InventoryRevision int64     `json:"inventory_revision"`
	CheckpointCurrent bool      `json:"checkpoint_current"`
	RestoredDisabled  bool      `json:"restored_disabled"`
	Expiring          int       `json:"expiring"`
	RemovableObjects  int       `json:"removable_objects"`
	Blocked           int       `json:"blocked"`
	Overdue           bool      `json:"overdue"`
	Cutoff            time.Time `json:"cutoff"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type savedRetentionJob struct {
	report      RetentionJobReport
	requestHash string
	plan        retention.Plan
}

type RetentionJobPage struct {
	Items      []RetentionJobReport `json:"items"`
	NextCursor string               `json:"next_cursor"`
}

// ListJobs loads a bounded page from durable storage, including cancelled and
// restored-disabled previews. The cursor belongs to this project and cannot
// provide an alternate SQL expression or access another project's records.
func (s *RetentionScanner) ListJobs(ctx context.Context, actorID, project, after string) (RetentionJobPage, error) {
	page := RetentionJobPage{Items: []RetentionJobReport{}}
	if !lowerHex32(project) || (after != "" && !lowerHex32(after)) {
		return page, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.jobTransaction(ctx, actorID, project)
	if err != nil {
		return page, err
	}
	defer tx.Rollback()
	query := "SELECT id,OCTET_LENGTH(plan) FROM retention_jobs WHERE project_id=?"
	args := []any{project}
	if after != "" {
		var at string
		if err := tx.QueryRowContext(ctx, "SELECT CAST(created_at AS CHAR) FROM retention_jobs WHERE project_id=? AND id=?", project, after).Scan(&at); err == sql.ErrNoRows {
			return page, domain.ErrForbidden
		} else if err != nil {
			return page, err
		}
		query += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, at, at, after)
	}
	rows, err := tx.QueryContext(ctx, query+" ORDER BY created_at DESC,id DESC LIMIT 51 FOR UPDATE", args...)
	if err != nil {
		return page, err
	}
	var ids []string
	bytes := int64(0)
	more := false
	for rows.Next() {
		var id string
		var size int64
		if err := rows.Scan(&id, &size); err != nil {
			rows.Close()
			return page, err
		}
		if size <= 0 || size > 16<<20 {
			rows.Close()
			return page, domain.ErrNotReady
		}
		if len(ids) == 50 || bytes+size > 32<<20 {
			more = true
			break
		}
		bytes += size
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return page, err
	}
	for _, id := range ids {
		saved, err := readRetentionJob(ctx, tx, project, id, false)
		if err != nil {
			return RetentionJobPage{}, err
		}
		page.Items = append(page.Items, saved.report)
	}
	if more {
		page.NextCursor = ids[len(ids)-1]
	}
	return page, tx.Commit()
}

// PlanJob scans real sources and files, then commits the immutable plan and its
// CURRENT checkpoint together. Retrying the same operation never rescans or
// silently replaces the author's original preview. A new preview needs a new ID.
func (s *RetentionScanner) PlanJob(ctx context.Context, actorID, project, operation string) (RetentionJobReport, error) {
	return s.planJob(ctx, actorID, project, operation, false)
}

func (s *RetentionScanner) planJob(ctx context.Context, actorID, project, operation string, automatic bool) (RetentionJobReport, error) {
	if !canonicalRequestKey(operation) || !lowerHex32(project) {
		return RetentionJobReport{}, domain.ErrValidation
	}
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil || s.Blobs == nil {
		return RetentionJobReport{}, domain.ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	requestHash := cleanupRequestHash(project, actorID, operation, automatic)
	tx, err := s.jobTransaction(ctx, actorID, project)
	if err != nil {
		return RetentionJobReport{}, err
	}
	defer tx.Rollback()
	existing, err := readRetentionJob(ctx, tx, project, operation, true)
	if err == nil {
		if existing.requestHash != requestHash {
			return RetentionJobReport{}, domain.ErrConflict
		}
		return existing.report, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return RetentionJobReport{}, err
	}
	if automatic {
		if err := automaticCleanupGuardTx(ctx, tx, actorID, project, operation); err != nil {
			return RetentionJobReport{}, err
		}
	}
	epoch, err := retention.BeginProjectScanTx(ctx, tx, protectedLedger(), project)
	if err != nil {
		return RetentionJobReport{}, domain.ErrNotReady
	}
	if err := tx.Commit(); err != nil {
		return RetentionJobReport{}, err
	}

	tx, err = s.jobTransaction(ctx, actorID, project)
	if err != nil {
		return RetentionJobReport{}, err
	}
	defer tx.Rollback()
	// A concurrent retry may have committed while this request was between
	// transactions. Return that exact result before considering a stale epoch.
	existing, err = readRetentionJob(ctx, tx, project, operation, true)
	if err == nil {
		if existing.requestHash != requestHash {
			return RetentionJobReport{}, domain.ErrConflict
		}
		return existing.report, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return RetentionJobReport{}, err
	}
	scan, err := retention.CompleteProjectScanTx(ctx, tx, protectedLedger(), project, epoch, inventorySource{s.Blobs})
	if err != nil {
		return RetentionJobReport{}, domain.ErrNotReady
	}
	if automatic {
		if err := automaticCleanupGuardTx(ctx, tx, actorID, project, operation); err != nil {
			return RetentionJobReport{}, err
		}
	}
	raw, err := json.Marshal(scan.Plan)
	if err != nil {
		return RetentionJobReport{}, err
	}
	if _, err := retention.ValidatePlanRecord(string(raw)); err != nil {
		return RetentionJobReport{}, domain.ErrNotReady
	}
	id := NewID()
	_, err = tx.ExecContext(ctx, `INSERT INTO retention_jobs(id,project_id,actor_id,operation_id,request_hash,inventory_revision,inventory_hash,plan_hash,plan,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))`, id, project, actorID, operation, requestHash, scan.Plan.InventoryRevision, scan.Plan.InventoryHash, scan.Plan.Hash, string(raw))
	if err != nil {
		return RetentionJobReport{}, databaseError(err)
	}
	saved, err := readRetentionJob(ctx, tx, project, id, false)
	if err != nil {
		return RetentionJobReport{}, err
	}
	return saved.report, tx.Commit()
}

func (s *RetentionScanner) ReadJob(ctx context.Context, actorID, project, id string) (RetentionJobReport, error) {
	if !lowerHex32(project) || !lowerHex32(id) {
		return RetentionJobReport{}, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.jobTransaction(ctx, actorID, project)
	if err != nil {
		return RetentionJobReport{}, err
	}
	defer tx.Rollback()
	saved, err := readRetentionJob(ctx, tx, project, id, false)
	if err == sql.ErrNoRows {
		return RetentionJobReport{}, domain.ErrForbidden
	}
	if err != nil {
		return RetentionJobReport{}, err
	}
	return saved.report, tx.Commit()
}

// CancelPlannedJob cancels a preview only. In-flight execution will require its
// own checkpoint-aware cancellation protocol; it cannot be cancelled here.
func (s *RetentionScanner) CancelPlannedJob(ctx context.Context, actorID, project, id, planHash string) (RetentionJobReport, error) {
	if !lowerHex32(project) || !lowerHex32(id) || len(planHash) != 64 {
		return RetentionJobReport{}, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.jobTransaction(ctx, actorID, project)
	if err != nil {
		return RetentionJobReport{}, err
	}
	defer tx.Rollback()
	saved, err := readRetentionJob(ctx, tx, project, id, false)
	if err == sql.ErrNoRows {
		return RetentionJobReport{}, domain.ErrForbidden
	}
	if err != nil {
		return RetentionJobReport{}, err
	}
	if saved.report.RestoredDisabled || saved.report.PlanHash != planHash || !slices.Contains([]string{"PLANNED", "CANCELLED"}, saved.report.State) {
		return RetentionJobReport{}, domain.ErrConflict
	}
	if saved.report.State == "PLANNED" {
		var receipts int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=?", id).Scan(&receipts); err != nil || receipts != 0 {
			return RetentionJobReport{}, domain.ErrNotReady
		}
		if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET state='CANCELLED',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=? AND state='PLANNED'", id, project); err != nil {
			return RetentionJobReport{}, err
		}
		saved, err = readRetentionJob(ctx, tx, project, id, false)
		if err != nil {
			return RetentionJobReport{}, err
		}
	}
	return saved.report, tx.Commit()
}

func (s *RetentionScanner) jobTransaction(ctx context.Context, actorID, project string) (*sql.Tx, error) {
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil {
		return nil, domain.ErrNotReady
	}
	tx, err := s.Workspace.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	if err := authorizeRetentionScan(ctx, tx, actorID, project); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := retention.CheckCleanupStorageTx(ctx, tx); err != nil {
		tx.Rollback()
		return nil, domain.ErrNotReady
	}
	return tx, nil
}

func readRetentionJob(ctx context.Context, tx *sql.Tx, project, key string, byOperation bool) (savedRetentionJob, error) {
	where := "j.id=?"
	if byOperation {
		where = "j.operation_id=?"
	}
	var saved savedRetentionJob
	var raw, inventoryHash, created, updated, cutoff, checkpointState, reasonCode string
	var revision int64
	var actor sql.NullString
	var operation string
	err := tx.QueryRowContext(ctx, `SELECT j.id,j.project_id,j.actor_id,j.operation_id,j.request_hash,j.inventory_revision,j.inventory_hash,j.plan_hash,
		IF(OCTET_LENGTH(j.plan)<=16777216,j.plan,''),j.state,j.restored_disabled,CAST(j.created_at AS CHAR),CAST(j.updated_at AS CHAR),j.reason_code,
		l.inventory_revision,l.state,COALESCE(CAST(l.scanned_at AS CHAR),'')
		FROM retention_jobs j JOIN project_lifecycle l ON l.project_id=j.project_id WHERE j.project_id=? AND `+where+` FOR UPDATE`, project, key).Scan(
		&saved.report.ID, &saved.report.ProjectID, &actor, &operation, &saved.requestHash, &saved.report.InventoryRevision, &inventoryHash, &saved.report.PlanHash,
		&raw, &saved.report.State, &saved.report.RestoredDisabled, &created, &updated, &reasonCode, &revision, &checkpointState, &cutoff)
	if err != nil {
		return saved, err
	}
	plan, err := retention.ValidatePlanRecord(raw)
	saved.report.Automatic = saved.requestHash == cleanupRequestHash(project, actor.String, operation, true)
	if err != nil || plan.ProjectID != project || plan.Hash != saved.report.PlanHash || plan.InventoryHash != inventoryHash || plan.InventoryRevision != saved.report.InventoryRevision || plan.InventoryRevision > revision || !lowerHex32(saved.report.ID) || !actor.Valid || !lowerHex32(actor.String) || !canonicalRequestKey(operation) || (!saved.report.Automatic && saved.requestHash != cleanupRequestHash(project, actor.String, operation, false)) {
		return savedRetentionJob{}, domain.ErrNotReady
	}
	if !slices.Contains([]string{"PLANNED", "DETACHING", "DELETING", "VERIFYING", "COMPLETE", "RETRY_PENDING", "OVERDUE", "BLOCKED", "CANCELLED"}, saved.report.State) {
		return savedRetentionJob{}, domain.ErrNotReady
	}
	saved.report.CreatedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", created, time.UTC)
	if err != nil || saved.report.CreatedAt.Before(plan.Cutoff) {
		return savedRetentionJob{}, domain.ErrNotReady
	}
	saved.report.UpdatedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", updated, time.UTC)
	if err != nil || saved.report.UpdatedAt.Before(saved.report.CreatedAt) {
		return savedRetentionJob{}, domain.ErrNotReady
	}
	checkpoint, clockErr := time.ParseInLocation("2006-01-02 15:04:05.999999", cutoff, time.UTC)
	saved.report.CheckpointCurrent = checkpointState == "CURRENT" && revision == plan.InventoryRevision && clockErr == nil && checkpoint.Equal(plan.Cutoff)
	saved.report.Expiring, saved.report.RemovableObjects, saved.report.Blocked = len(plan.Expire), len(plan.DeleteObjects), len(plan.Blocked)
	saved.report.Overdue, saved.report.Cutoff = plan.Overdue, plan.Cutoff
	saved.report.Reason = cleanupReason(reasonCode)
	saved.plan = plan
	return saved, nil
}

// Closed messages prevent private SQL/source details from reaching status UI.
func cleanupReason(code string) string {
	switch code {
	case "", "OBJECT_DELETE_PREPARED":
		return ""
	case "SOURCE_HAS_LIVE_DEPENDENTS", "OBJECT_NO_LONGER_ELIGIBLE", "PLAN_HAS_BLOCKED_ITEMS":
		return "部分材料仍被使用或仍在保留期内；处理依赖后重试，或重新生成预览。"
	case "SOURCE_CHECKPOINT_CHANGED", "SOURCE_VERSION_CHANGED":
		return "资料或引用已变化，请重新生成预览。"
	case "SOURCE_STEP_FAILED", "OBJECT_STEP_FAILED":
		return "存储操作暂未完成，后台将从已保存的进度重试。"
	case "EXECUTION_UNAVAILABLE":
		return "清理权限或任务记录已变化；恢复权限后可重试，恢复副本中的历史任务不能执行。"
	default:
		return "材料或清理记录未通过核对，已停止后续清理；请检查任务后重新生成预览。"
	}
}
