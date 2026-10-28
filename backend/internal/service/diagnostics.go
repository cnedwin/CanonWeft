package service

import (
	"context"
	"runtime"
	"time"

	"novelstudio/internal/domain"
)

// Diagnostics deliberately contains only fixed metadata and aggregate counters.
// No filenames, project/user IDs, credentials, prompts, bodies or raw errors.
type OperationsReport struct {
	Format          int                         `json:"format"`
	GeneratedAt     time.Time                   `json:"generated_at"`
	SchemaVersion   int                         `json:"schema_version"`
	GoVersion       string                      `json:"go_version"`
	ActiveProjects  int64                       `json:"active_projects"`
	DeletedProjects int64                       `json:"deleted_projects"`
	States          map[string]map[string]int64 `json:"states"`
}

func (s *Workspace) OperationsDiagnostics(ctx context.Context, actorID string) (OperationsReport, error) {
	report := OperationsReport{Format: 1, GeneratedAt: time.Now().UTC(), GoVersion: runtime.Version(), States: map[string]map[string]int64{}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return report, err
	}
	defer tx.Rollback()
	a, err := s.actorTx(ctx, tx, actorID)
	if err != nil {
		return report, err
	}
	if a.Role != "owner" {
		return report, domain.ErrForbidden
	}
	if err = tx.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&report.SchemaVersion); err != nil {
		return report, err
	}
	if err = tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(deleted_at IS NULL),0),COALESCE(SUM(deleted_at IS NOT NULL),0) FROM projects WHERE workspace_id=?", a.WorkspaceID).Scan(&report.ActiveProjects, &report.DeletedProjects); err != nil {
		return report, err
	}
	for _, table := range []string{"model_calls", "generation_tasks", "platform_tasks", "index_jobs", "tool_trials", "qualification_runs", "semantic_review_runs"} {
		// Both the table identifiers and state labels are fixed by the program.
		// Unexpected stored values become OTHER, never exported as raw text.
		state := `CASE WHEN t.state IN ('PREPARED','QUEUED','PENDING','RUNNING','DISPATCHED','RESERVED','UNKNOWN','BLOCKED','FAILED','CANCELLED','SUCCEEDED','COMPLETED','READY','NEEDS_REVIEW') THEN t.state ELSE 'OTHER' END`
		rows, err := tx.QueryContext(ctx, "SELECT "+state+",COUNT(*) FROM "+table+" t JOIN projects p ON p.id=t.project_id WHERE p.workspace_id=? GROUP BY "+state, a.WorkspaceID)
		if err != nil {
			return report, err
		}
		counts := map[string]int64{}
		for rows.Next() {
			var status string
			var count int64
			if err = rows.Scan(&status, &count); err != nil {
				rows.Close()
				return report, err
			}
			counts[status] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, err
		}
		report.States[table] = counts
	}
	if err := audit(ctx, tx, a, "diagnostics.export", ""); err != nil {
		return report, err
	}
	return report, tx.Commit()
}
