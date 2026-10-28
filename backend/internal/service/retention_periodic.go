package service

import (
	"context"
	"database/sql"
	"novelstudio/internal/domain"
	"os"
	"time"
)

type PeriodicCleanupResult struct {
	Cursor    string `json:"cursor"`
	Done      bool   `json:"done"`
	ProjectID string `json:"project_id,omitempty"`
	JobID     string `json:"job_id,omitempty"`
	State     string `json:"state"`
}

func periodicCleanupConfigured() bool {
	return os.Getenv("STUDIO_RETENTION_EXECUTION_ENABLED") == "1" && os.Getenv("STUDIO_RETENTION_AUTO_ENABLED") == "1"
}

func (w *Workspace) PeriodicCleanupAvailable(ctx context.Context, workspace string) (bool, error) {
	if !periodicCleanupConfigured() {
		return false, nil
	}
	actor := os.Getenv("STUDIO_RETENTION_AUTO_ACTOR")
	if !lowerHex32(actor) {
		return false, nil
	}
	var count int
	err := w.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE id=? AND workspace_id=? AND enabled=TRUE AND role='owner'", actor, workspace).Scan(&count)
	return err == nil && count == 1, err
}

func automaticCleanupAllowed(actor string) bool {
	return periodicCleanupConfigured() && lowerHex32(actor) && os.Getenv("STUDIO_RETENTION_AUTO_ACTOR") == actor
}

func cleanupRequestHash(project, actor, operation string, automatic bool) string {
	prefix := "retention-plan-v1/"
	if automatic {
		prefix = "retention-periodic-plan-v1/"
	}
	return digest(prefix + project + "/" + actor + "/" + operation)
}

// All callers hold the existing project coordination lock. A scheduler cannot
// take over a manual preview or restart a held job, even across process retries.
func automaticCleanupGuardTx(ctx context.Context, tx *sql.Tx, actor, project, operation string) error {
	if !automaticCleanupAllowed(actor) {
		return domain.ErrForbidden
	}
	var competing int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM retention_jobs WHERE project_id=? AND operation_id<>? AND restored_disabled=0 AND state NOT IN ('COMPLETE','CANCELLED')`, project, operation).Scan(&competing)
	if err != nil {
		return err
	}
	if competing != 0 {
		return domain.ErrConflict
	}
	return nil
}

// PeriodicCleanupStep visits one project, never a caller-supplied actor. SQL
// supplies the day and checks the fixed configured owner's current authority.
// Actual erasure remains in the existing confirmed-job execution workflow.
func (s *RetentionScanner) PeriodicCleanupStep(ctx context.Context, after string) (PeriodicCleanupResult, error) {
	result := PeriodicCleanupResult{Cursor: after}
	if !periodicCleanupConfigured() {
		result.State, result.Done = "PAUSED", true
		return result, nil
	}
	actor := os.Getenv("STUDIO_RETENTION_AUTO_ACTOR")
	if !lowerHex32(actor) || (after != "" && !lowerHex32(after)) {
		return result, domain.ErrValidation
	}
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil || s.Blobs == nil {
		return result, domain.ErrNotReady
	}
	ctx, cancel := context.WithTimeout(ctx, 75*time.Second)
	defer cancel()
	var workspace, project string
	err := s.Workspace.DB.QueryRowContext(ctx, "SELECT workspace_id FROM users WHERE id=? AND enabled=TRUE AND role='owner'", actor).Scan(&workspace)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	err = s.Workspace.DB.QueryRowContext(ctx, `SELECT p.id FROM projects p LEFT JOIN project_lifecycle l ON l.project_id=p.id WHERE p.workspace_id=? AND p.id>? AND (l.state IS NULL OR l.state<>'PURGED') ORDER BY p.id LIMIT 1`, workspace, after).Scan(&project)
	if err == sql.ErrNoRows {
		result.State, result.Done = "COMPLETE", true
		return result, nil
	}
	if err != nil {
		return result, err
	}
	result.ProjectID, result.Cursor = project, project
	tx, err := s.jobTransaction(ctx, actor, project)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var day string
	if err = tx.QueryRowContext(ctx, "SELECT DATE_FORMAT(UTC_TIMESTAMP(),'%Y-%m-%d')").Scan(&day); err != nil {
		return result, err
	}
	operation := "request-v2:" + digest("periodic-cleanup-v1/" + actor + "/" + project + "/" + day)[:32]
	current, err := readRetentionJob(ctx, tx, project, operation, true)
	if err == nil {
		result.JobID, result.State = current.report.ID, current.report.State
		if !current.report.Automatic || current.report.RestoredDisabled {
			result.State = "HELD"
			return result, tx.Commit()
		}
		if current.report.State != "PLANNED" || current.report.Blocked > 0 || current.report.Overdue {
			return result, tx.Commit()
		}
	} else if err != sql.ErrNoRows {
		return result, err
	}
	var held string
	err = tx.QueryRowContext(ctx, `SELECT id FROM retention_jobs WHERE project_id=? AND operation_id<>? AND restored_disabled=0 AND state NOT IN ('COMPLETE','CANCELLED') ORDER BY created_at,id LIMIT 1`, project, operation).Scan(&held)
	if err == nil {
		result.State, result.JobID = "HELD", held
		return result, tx.Commit()
	}
	if err != sql.ErrNoRows {
		return result, err
	}
	if err = tx.Commit(); err != nil {
		return result, err
	}
	if result.JobID == "" {
		current.report, err = s.planJob(ctx, actor, project, operation, true)
		if err != nil {
			return result, err
		}
		result.JobID = current.report.ID
	}
	job, err := s.startJob(ctx, actor, project, result.JobID, current.report.PlanHash, true)
	if err != nil {
		return result, err
	}
	result.State = job.State
	return result, nil
}
