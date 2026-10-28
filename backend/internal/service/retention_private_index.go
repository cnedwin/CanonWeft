package service

import (
	"context"
	"database/sql"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// Refusal is conservative: no old index may become readable or overwrite its
// expiry journal, even when only one chunk field has been cleared so far.
// Both tables use the catalog's single lowercase hash primary key; OwnerKey
// encodes that one value as a compact JSON array, without spaces.
func requirePrivateIndexLiveTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return requireIndexLiveTx(ctx, tx, project, id, false)
}

func requirePublishedIndexLiveTx(ctx context.Context, tx *sql.Tx, project, id string) error {
	return requireIndexLiveTx(ctx, tx, project, id, true)
}

func requireIndexLiveTx(ctx context.Context, tx *sql.Tx, project, id string, published bool) error {
	indexTable, chunkTable := "working_indexes", "working_chunks"
	if published {
		indexTable, chunkTable = "knowledge_indexes", "knowledge_chunks"
	}
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", retention.MigrationName).Scan(&installed); err != nil {
		return err
	}
	if installed == 0 {
		var present int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('retention_references','retention_evidence')").Scan(&present); err != nil || present != 0 {
			return domain.ErrNotReady
		}
		return nil
	}
	if installed != 1 {
		return domain.ErrNotReady
	}
	key, err := retention.RowOwnerKey(indexTable, map[string]string{"id": id})
	if err != nil {
		return domain.ErrNotReady
	}
	var expired bool
	err = tx.QueryRowContext(ctx, `SELECT EXISTS(
 SELECT 1 FROM retention_references r WHERE r.project_id=? AND r.state='EXPIRED' AND
	((r.owner_table='`+indexTable+`' AND r.owner_key=?) OR
	(r.owner_table='`+chunkTable+`' AND EXISTS(SELECT 1 FROM `+chunkTable+` c WHERE c.index_id=? AND r.owner_key=CONCAT('["',c.id,'"]'))))
)`, project, key, id).Scan(&expired)
	if err != nil {
		return err
	}
	if expired {
		return domain.ErrMaterialExpired
	}
	return nil
}
