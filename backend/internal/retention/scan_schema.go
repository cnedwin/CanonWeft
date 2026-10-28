package retention

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"novelstudio/migrations"
)

// Stronger than observation-only registration: CURRENT requires actual36
// execution and the complete compiled history, never a test/partial marker.
func verifyScanSchema(ctx context.Context, tx *sql.Tx) error {
	_, pendingHash, err := migrations.PendingRetentionSQL()
	if err != nil {
		return err
	}
	var version int
	var checksum string
	var initialized bool
	if err := tx.QueryRowContext(ctx, "SELECT version,checksum,history_initialized FROM schema_version WHERE id=1 FOR SHARE").Scan(&version, &checksum, &initialized); err != nil || (version < 36 || version > 38) || !initialized {
		return ErrInvalidInventory
	}
	expectedHash := pendingHash
	if version >= 37 {
		_, expectedHash, err = migrations.NovelEntrySQL()
		if err != nil {
			return err
		}
	}
	if version == 38 {
		_, expectedHash, err = migrations.NovelSettingsSQL()
		if err != nil {
			return err
		}
	}
	if checksum != expectedHash {
		return ErrInvalidInventory
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return err
	}
	type migration struct{ name, checksum string }
	var expected []migration
	for _, entry := range entries {
		if entry.Name() == migrations.NovelEntryName || entry.Name() == migrations.NovelSettingsName {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		if !strings.HasPrefix(entry.Name(), fmt.Sprintf("%04d_", len(expected)+1)) {
			return ErrInvalidInventory
		}
		raw, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			return err
		}
		expected = append(expected, migration{entry.Name(), financialDigest([]byte(strings.ReplaceAll(string(raw), "\r\n", "\n")))})
	}
	if len(expected) != 35 {
		return ErrInvalidInventory
	}
	expected = append(expected, migration{MigrationName, pendingHash})
	if version >= 37 {
		_, entryHash, e := migrations.NovelEntrySQL()
		if e != nil {
			return e
		}
		expected = append(expected, migration{migrations.NovelEntryName, entryHash})
	}
	if version == 38 {
		expected = append(expected, migration{migrations.NovelSettingsName, expectedHash})
	}
	rows, err := tx.QueryContext(ctx, "SELECT version,name,checksum,origin FROM schema_migrations ORDER BY version FOR SHARE")
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var actualVersion int
		var name, hash, origin string
		if err := rows.Scan(&actualVersion, &name, &hash, &origin); err != nil {
			return err
		}
		if count >= len(expected) || actualVersion != count+1 || name != expected[count].name || hash != expected[count].checksum || (origin != "EXECUTED" && origin != "LEGACY_BASELINE") || (actualVersion >= 36 && origin != "EXECUTED") {
			return ErrInvalidInventory
		}
		count++
	}
	if rows.Err() != nil || count != len(expected) {
		return ErrInvalidInventory
	}
	if err := rows.Close(); err != nil {
		return err
	}
	return verifyScanCheckpointLayout(ctx, tx)
}

func verifyScanCheckpointLayout(ctx context.Context, tx *sql.Tx) error {
	// DATETIME(6), binary identity and the complete compound object key are
	// essential to age/lease checks. Header checksums do not prove live DDL.
	shapes := map[string]string{
		"project_lifecycle.project_id":         "char(32)|NO|ascii_bin",
		"project_lifecycle.source_ledger_id":   "char(32)|NO|ascii_bin",
		"project_lifecycle.delete_sequence":    "bigint unsigned|YES|",
		"project_lifecycle.time_basis":         "enum('SOURCE_UTC','LEGACY_REGISTERED')|NO|",
		"project_lifecycle.inventory_revision": "bigint unsigned|NO|",
		"project_lifecycle.catalog_version":    "int unsigned|NO|",
		"project_lifecycle.state":              "enum('UNSCANNED','CURRENT','PURGING','PURGED','OVERDUE','BLOCKED')|NO|",
		"retention_objects.project_id":         "char(32)|NO|ascii_bin",
		"retention_objects.object_hash":        "char(64)|NO|ascii_bin",
		"retention_objects.state":              "enum('PRESENT','DELETING','DELETED','BLOCKED')|NO|",
		"retention_objects.revision":           "bigint unsigned|NO|",
	}
	for _, key := range []string{"project_lifecycle.first_verified_at", "project_lifecycle.updated_at", "retention_objects.first_seen_at"} {
		shapes[key] = "datetime(6)|NO|"
	}
	for _, key := range []string{"project_lifecycle.source_deleted_at", "project_lifecycle.deleted_at", "project_lifecycle.recover_until", "project_lifecycle.cleanup_eligible_at", "project_lifecycle.online_deadline", "project_lifecycle.backup_deadline", "project_lifecycle.scanned_at", "retention_objects.lease_until", "retention_objects.deleted_at"} {
		shapes[key] = "datetime(6)|YES|"
	}
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLLATION_NAME,'') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name IN ('project_lifecycle','retention_objects')")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, column, kind, nullable, collation string
		if err := rows.Scan(&table, &column, &kind, &nullable, &collation); err != nil {
			return err
		}
		if strings.HasPrefix(kind, "enum(") {
			collation = ""
		}
		key := table + "." + column
		if shapes[key] != kind+"|"+nullable+"|"+collation {
			return ErrInvalidInventory
		}
		delete(shapes, key)
	}
	if rows.Err() != nil || len(shapes) != 0 {
		return ErrInvalidInventory
	}
	if err := rows.Close(); err != nil {
		return err
	}
	keys, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name IN ('project_lifecycle','retention_objects') AND constraint_name='PRIMARY' ORDER BY TABLE_NAME,ORDINAL_POSITION")
	if err != nil {
		return err
	}
	defer keys.Close()
	var actual []string
	for keys.Next() {
		var table, column string
		if err := keys.Scan(&table, &column); err != nil {
			return err
		}
		actual = append(actual, table+"."+column)
	}
	if keys.Err() != nil || strings.Join(actual, ",") != "project_lifecycle.project_id,retention_objects.project_id,retention_objects.object_hash" {
		return ErrInvalidInventory
	}
	return nil
}
