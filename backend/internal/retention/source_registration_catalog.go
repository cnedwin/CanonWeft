package retention

import (
	"context"
	"database/sql"
	"slices"
	"strings"
)

// These are explicit metadata layouts, not permission to ignore extra tables.
// The pending migration is checked as a whole before any registration writes.
func registrationMetadata() map[string][]string {
	result := map[string][]string{}
	for table, columns := range map[string]string{
		"retention_recovery_attempts":  "target_project_id project_id actor_id storage_namespace objects state restored_disabled created_at updated_at",
		"project_lifecycle":            "project_id source_ledger_id delete_sequence time_basis source_deleted_at first_verified_at deleted_at recover_until cleanup_eligible_at online_deadline backup_deadline inventory_revision catalog_version scanned_at state updated_at",
		"retention_references":         "id project_id owner_table owner_key field_key class storage_kind object_hash payload_hash source_created_at time_basis first_verified_at deadline state expired_at content_root accounting_open lease_until revision updated_at",
		"retention_objects":            "project_id object_hash first_seen_at lease_until state revision deleted_at",
		"retention_evidence":           "id project_id reference_id reference_revision original_payload_hash projection_hash projection expired_at",
		"retention_jobs":               "id project_id actor_id operation_id request_hash inventory_revision inventory_hash plan_hash plan state cursor_key attempt reason_code restored_disabled created_at updated_at",
		"retention_receipts":           "job_id item_key phase result evidence_hash recorded_at",
		"retention_identity_policies":  "workspace_id legacy_writes_closed revision closed_at created_at",
		"retention_operation_keys":     "workspace_id actor_id owner_table original_key_hash original_owner_key anonymous_key source_id request_hash operation_kind created_at",
		"retention_source_slots":       "id project_id owner_table owner_key field_key time_basis first_verified_at",
		"retention_source_values":      "source_id payload_hash is_null first_verified_at",
		"retention_reference_versions": "reference_id revision project_id source_slot_id source_payload_hash record_hash snapshot recorded_at",
	} {
		result[table] = strings.Fields(columns)
	}
	return result
}

// RegistrationMetadata returns a fresh copy of the exact SQL layouts shared
// by registration and archival transport. This does not activate a migration.
func RegistrationMetadata() map[string][]string {
	return registrationMetadata()
}

func verifyRegistrationCatalog(ctx context.Context, tx *sql.Tx) error {
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", MigrationName).Scan(&installed); err != nil || installed != 1 {
		return ErrSourceOwnership
	}
	if err := verifySourceCatalog(ctx, tx, registrationMetadata()); err != nil {
		return err
	}
	// Source clocks must be literal UTC DATETIME values, never connection-local
	// TIMESTAMP conversions. Binary identity and exact primary keys are required.
	shapes := map[string]string{
		"retention_source_slots.id":                        "char(64)|ascii_bin",
		"retention_source_slots.project_id":                "char(32)|ascii_bin",
		"retention_source_slots.owner_table":               "varchar(64)|ascii_bin",
		"retention_source_slots.owner_key":                 "varchar(256)|ascii_bin",
		"retention_source_slots.field_key":                 "varchar(64)|ascii_bin",
		"retention_source_slots.time_basis":                "enum('LEGACY_REGISTERED')|",
		"retention_source_slots.first_verified_at":         "datetime(6)|",
		"retention_source_values.source_id":                "char(64)|ascii_bin",
		"retention_source_values.payload_hash":             "char(64)|ascii_bin",
		"retention_source_values.is_null":                  "tinyint(1)|",
		"retention_source_values.first_verified_at":        "datetime(6)|",
		"retention_references.id":                          "char(64)|ascii_bin",
		"retention_references.project_id":                  "char(32)|ascii_bin",
		"retention_references.owner_table":                 "varchar(64)|ascii_bin",
		"retention_references.owner_key":                   "varchar(256)|ascii_bin",
		"retention_references.field_key":                   "varchar(128)|ascii_bin",
		"retention_references.class":                       "varchar(24)|ascii_bin",
		"retention_references.storage_kind":                "enum('BLOB','INLINE','METADATA')|",
		"retention_references.object_hash":                 "char(64)|ascii_bin",
		"retention_references.payload_hash":                "char(64)|ascii_bin",
		"retention_references.source_created_at":           "datetime(6)|",
		"retention_references.time_basis":                  "enum('SOURCE_UTC','LEGACY_REGISTERED')|",
		"retention_references.first_verified_at":           "datetime(6)|",
		"retention_references.deadline":                    "datetime(6)|",
		"retention_references.state":                       "enum('RETAINED','EXPIRED')|",
		"retention_references.expired_at":                  "datetime(6)|",
		"retention_references.content_root":                "tinyint(1)|",
		"retention_references.accounting_open":             "tinyint(1)|",
		"retention_references.lease_until":                 "datetime(6)|",
		"retention_references.revision":                    "bigint unsigned|",
		"retention_references.updated_at":                  "datetime(6)|",
		"retention_reference_versions.reference_id":        "char(64)|ascii_bin",
		"retention_reference_versions.revision":            "bigint unsigned|",
		"retention_reference_versions.project_id":          "char(32)|ascii_bin",
		"retention_reference_versions.source_slot_id":      "char(64)|ascii_bin",
		"retention_reference_versions.source_payload_hash": "char(64)|ascii_bin",
		"retention_reference_versions.record_hash":         "char(64)|ascii_bin",
		"retention_reference_versions.snapshot":            "mediumtext|",
		"retention_reference_versions.recorded_at":         "datetime(6)|",
	}
	nullableColumns := map[string]bool{"retention_references.object_hash": true, "retention_references.deadline": true, "retention_references.expired_at": true, "retention_references.lease_until": true}
	rows, err := tx.QueryContext(ctx, "SELECT TABLE_NAME,COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLLATION_NAME,'') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name IN ('retention_source_slots','retention_source_values','retention_references','retention_reference_versions')")
	if err != nil {
		return err
	}
	for rows.Next() {
		var table, column, kind, nullable, collation string
		if err := rows.Scan(&table, &column, &kind, &nullable, &collation); err != nil {
			rows.Close()
			return err
		}
		key := table + "." + column
		if strings.HasPrefix(kind, "enum(") || key == "retention_reference_versions.snapshot" {
			collation = "" // Closed ENUM values and exact TEXT bytes are checked independently.
		}
		wantNullable := "NO"
		if nullableColumns[key] {
			wantNullable = "YES"
		}
		if shapes[key] != kind+"|"+collation || nullable != wantNullable {
			rows.Close()
			return ErrSourceOwnership
		}
		delete(shapes, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(shapes) != 0 {
		return ErrSourceOwnership
	}
	for table, expected := range map[string][]string{
		"retention_source_slots": {"id"}, "retention_source_values": {"source_id", "payload_hash", "is_null"},
		"retention_references": {"id"}, "retention_reference_versions": {"reference_id", "revision"},
	} {
		rows, err = tx.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name=? AND constraint_name='PRIMARY' ORDER BY ORDINAL_POSITION", table)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return err
			}
			names = append(names, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil || !slices.Equal(names, expected) {
			return ErrSourceOwnership
		}
	}
	return nil
}
