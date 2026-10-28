package backup

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"time"
)

// Only called for the newly-created isolated restore database after installing
// the independently qualified schema35 baseline for legacy archive compatibility.
// SQL comes from the compiled contract, never from an archive or user input.
func installRetentionRestoreSchema(ctx context.Context, db *sql.DB) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	defer conn.Raw(func(any) error { return driver.ErrBadConn })
	var locked int
	if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT(DATABASE(), ':schema'),10)").Scan(&locked); err != nil || locked != 1 {
		return errors.New("restore schema lock unavailable")
	}
	if _, err = readHistory(ctx, conn, supportedSchemaVersion); err != nil {
		return err
	}
	if err = checkTables(ctx, conn, supportedSchemaVersion); err != nil {
		return err
	}
	migration, ddl, err := retentionMigration()
	if err != nil {
		return err
	}
	for _, statement := range strings.Split(ddl, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err = conn.ExecContext(ctx, statement); err != nil {
			return errors.New("isolated retention schema installation failed")
		}
	}
	if err = checkTables(ctx, conn, retentionSchemaVersion); err != nil {
		return err
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "INSERT INTO schema_migrations(version,name,checksum,origin) VALUES(?,?,?,'EXECUTED')", migration.Version, migration.Name, migration.SHA256); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE schema_version SET version=?,checksum=?,history_initialized=TRUE WHERE id=1", migration.Version, migration.SHA256); err != nil {
		return err
	}
	return tx.Commit()
}

func invalidateRestoredInventory(data Data) {
	for _, spec := range retentionTableCatalog() {
		if spec.name != "project_lifecycle" {
			continue
		}
		for _, row := range data[spec.name] {
			// Preserve original age/deletion/deadline fields and revisions.
			// Disabled imported jobs cannot consume the old plan; a new full
			// scan must establish currentness before any cleanup can proceed.
			spec.set(row, "state", "UNSCANNED")
			row[spec.index("scanned_at")] = nil
		}
	}
}

// Verify actual SQL representations after insertion and current revocations,
// before committing recovery. SQL/JSON conversions must not change source hashes.
func validateRestoredRetentionState(ctx context.Context, tx *sql.Tx, original *Archive) error {
	catalog, err := snapshotCatalog(original.Manifest.Schema)
	if err != nil {
		return err
	}
	data := Data{}
	for _, spec := range catalog {
		rows, err := readRows(ctx, tx, spec)
		if err != nil {
			return err
		}
		data[spec.name] = rows
	}
	var clock string
	if err = tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return err
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return err
	}
	business, metadata, err := splitRetentionData(original.Manifest.Schema, data)
	if err != nil {
		return err
	}
	manifest := original.Manifest
	manifest.SnapshotAt = at
	refs, err := validatedRetentionReferences(&Archive{Manifest: manifest, Data: data}, business, metadata)
	if err != nil {
		return err
	}
	if err = validateBlobReferenceSet(refs, original.Blobs); err != nil {
		return err
	}
	return validateMemoryObjects(data, original.Blobs, true)
}
