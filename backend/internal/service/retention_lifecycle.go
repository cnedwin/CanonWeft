package service

import (
	"context"
	"database/sql"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// Pending DDL is not activation. Once the named retention migration is recorded,
// missing or corrupt lifecycle storage must fail the whole deletion transaction.
func registerProjectDeletionLifecycleTx(ctx context.Context, tx *sql.Tx, project string) error {
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", retention.MigrationName).Scan(&installed); err != nil {
		return err
	}
	if installed == 0 {
		return nil
	}
	if installed != 1 {
		return domain.ErrNotReady
	}
	_, err := retention.RegisterProjectLifecycle(ctx, tx, protectedLedger(), project)
	if err != nil {
		return domain.ErrNotReady
	}
	return nil
}
