package service

import (
	"context"
	"database/sql"
	"os"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

// RegisterRetentionSources records observations and versioned references, never
// expires content or settles balances. Its exact migration gate
// remains closed on the current production schema. A workspace owner may also
// register deleted projects, without restoring their read or execution access.
func (s *Workspace) RegisterRetentionSources(ctx context.Context, actorID, project string) (retention.SourceRegistrationReport, error) {
	var result retention.SourceRegistrationReport
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var workspace, role string
	err = tx.QueryRowContext(ctx, "SELECT workspace_id,role FROM users WHERE id=? AND enabled=TRUE FOR SHARE", actorID).Scan(&workspace, &role)
	if err == sql.ErrNoRows || (err == nil && role != "owner") {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	var owned string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? AND workspace_id=? FOR UPDATE", project, workspace).Scan(&owned)
	if err == sql.ErrNoRows {
		return result, domain.ErrForbidden
	}
	if err != nil {
		return result, err
	}
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	result, err = retention.RegisterSourceObservations(ctx, tx, protectedLedger(), project, blobs.ReadObject)
	if err != nil {
		return retention.SourceRegistrationReport{}, domain.ErrNotReady
	}
	if err := tx.Commit(); err != nil {
		return retention.SourceRegistrationReport{}, err
	}
	return result, nil
}
