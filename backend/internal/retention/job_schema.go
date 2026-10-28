package retention

import (
	"context"
	"database/sql"
	"strings"
)

// CheckCleanupStorageTx requires the real pending schema before a durable plan
// can be read or saved. It neither installs the schema nor authorizes execution.
func CheckCleanupStorageTx(ctx context.Context, tx *sql.Tx) error {
	if tx == nil {
		return ErrInvalidInventory
	}
	if err := verifyScanSchema(ctx, tx); err != nil {
		return err
	}
	if err := verifyRegistrationCatalog(ctx, tx); err != nil {
		return err
	}
	shapes := map[string]string{
		"id": "char(32)|NO|ascii_bin", "project_id": "char(32)|NO|ascii_bin",
		"actor_id": "char(32)|YES|ascii_bin", "operation_id": "varchar(120)|NO|",
		"request_hash": "char(64)|NO|ascii_bin", "inventory_revision": "bigint unsigned|NO|",
		"inventory_hash": "char(64)|NO|ascii_bin", "plan_hash": "char(64)|NO|ascii_bin",
		"plan": "mediumtext|NO|", "cursor_key": "varchar(128)|NO|ascii_bin",
		"attempt": "int unsigned|NO|", "reason_code": "varchar(64)|NO|ascii_bin",
		"restored_disabled": "tinyint(1)|NO|", "created_at": "datetime(6)|NO|",
		"updated_at": "datetime(6)|NO|",
		"state":      "enum('PLANNED','DETACHING','DELETING','VERIFYING','COMPLETE','RETRY_PENDING','OVERDUE','BLOCKED','CANCELLED')|NO|",
	}
	rows, err := tx.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLLATION_NAME,'') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='retention_jobs'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var column, kind, nullable, collation string
		if err := rows.Scan(&column, &kind, &nullable, &collation); err != nil {
			return err
		}
		if column == "operation_id" || column == "plan" || column == "state" {
			collation = "" // Closed canonical operation IDs; plan bytes are independently hashed.
		}
		if shapes[column] != kind+"|"+nullable+"|"+collation {
			return ErrInvalidInventory
		}
		delete(shapes, column)
	}
	if rows.Err() != nil || len(shapes) != 0 {
		return ErrInvalidInventory
	}
	if err := rows.Close(); err != nil {
		return err
	}
	keys, err := tx.QueryContext(ctx, "SELECT INDEX_NAME,COLUMN_NAME FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name='retention_jobs' AND NON_UNIQUE=0 ORDER BY INDEX_NAME,SEQ_IN_INDEX")
	if err != nil {
		return err
	}
	defer keys.Close()
	indexes := map[string][]string{}
	for keys.Next() {
		var index, column string
		if err := keys.Scan(&index, &column); err != nil {
			return err
		}
		indexes[index] = append(indexes[index], column)
	}
	if keys.Err() != nil || len(indexes) != 2 || strings.Join(indexes["PRIMARY"], ",") != "id" {
		return ErrInvalidInventory
	}
	for index, columns := range indexes {
		if index != "PRIMARY" && strings.Join(columns, ",") != "project_id,operation_id" {
			return ErrInvalidInventory
		}
	}
	if err := keys.Close(); err != nil {
		return err
	}
	return checkReceiptSchema(ctx, tx)
}

func checkReceiptSchema(ctx context.Context, tx *sql.Tx) error {
	shapes := map[string]string{
		"job_id": "char(32)|NO|ascii_bin", "item_key": "varchar(128)|NO|ascii_bin",
		"phase": "enum('DETACHED','REMOVED','VERIFIED')|NO|", "result": "enum('SUCCEEDED','ABSENT','BLOCKED')|NO|",
		"evidence_hash": "char(64)|NO|ascii_bin", "recorded_at": "datetime(6)|NO|",
	}
	rows, err := tx.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COALESCE(COLLATION_NAME,'') FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name='retention_receipts'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var column, kind, nullable, collation string
		if err := rows.Scan(&column, &kind, &nullable, &collation); err != nil {
			return err
		}
		if column == "phase" || column == "result" {
			collation = ""
		}
		if shapes[column] != kind+"|"+nullable+"|"+collation {
			return ErrInvalidInventory
		}
		delete(shapes, column)
	}
	if rows.Err() != nil || len(shapes) != 0 {
		return ErrInvalidInventory
	}
	if err := rows.Close(); err != nil {
		return err
	}
	keys, err := tx.QueryContext(ctx, "SELECT COLUMN_NAME FROM information_schema.key_column_usage WHERE table_schema=DATABASE() AND table_name='retention_receipts' AND constraint_name='PRIMARY' ORDER BY ORDINAL_POSITION")
	if err != nil {
		return err
	}
	defer keys.Close()
	var columns []string
	for keys.Next() {
		var column string
		if err := keys.Scan(&column); err != nil {
			return err
		}
		columns = append(columns, column)
	}
	if keys.Err() != nil || strings.Join(columns, ",") != "job_id,item_key,phase" {
		return ErrInvalidInventory
	}
	return nil
}
