package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// A request key is separate from its request fingerprint. Retiring a key must
// resolve the original row; it must never alter or recompute that fingerprint.
type requestIdentity struct {
	Key, ID, Hash, Kind string
}

func canonicalRequestKey(key string) bool {
	return strings.HasPrefix(key, "request-v2:") && lowerHex32(strings.TrimPrefix(key, "request-v2:"))
}

func lowerHex32(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func internalRequestKey(key string) bool {
	for _, prefix := range []string{"generation-", "evaluation-", "ingest-", "recipe-", "semantic-review-"} {
		if strings.HasPrefix(key, prefix) && lowerHex32(strings.TrimPrefix(key, prefix)) {
			return true
		}
	}
	return false
}

// Fixed SQL only: table and column names are never supplied by an HTTP client.
func requestIdentitySQL(table string) string {
	switch table {
	case "operations":
		return "SELECT o.operation_id,o.result_id,o.request_hash,o.kind FROM operations o JOIN users u ON u.id=o.actor_id WHERE u.workspace_id=? AND o.actor_id=? AND o.operation_id=? FOR UPDATE"
	case "model_calls":
		return "SELECT operation_id,id,input_hash,'' FROM model_calls WHERE workspace_id=? AND actor_id=? AND operation_id=? FOR UPDATE"
	case "source_imports":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM source_imports s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE"
	case "ingest_jobs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM ingest_jobs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE"
	case "generation_tasks":
		return "SELECT operation_id,id,request_hash,'' FROM generation_tasks WHERE workspace_id=? AND actor_id=? AND operation_id=? FOR UPDATE"
	case "recipe_runs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM recipe_runs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE"
	case "semantic_review_runs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM semantic_review_runs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE"
	default:
		return ""
	}
}

// Partial installs fail closed. Activation follows the named retention migration,
// never an assumed future schema number (schema35 is the memory foundation).
func requestIdentityPolicyTx(ctx context.Context, tx *sql.Tx, workspace string) (enabled, closed bool, err error) {
	var count int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('retention_identity_policies','retention_operation_keys')").Scan(&count)
	if err != nil {
		return
	}
	if count == 0 {
		var activated int
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", retention.MigrationName).Scan(&activated)
		if err != nil {
			return
		}
		if activated == 0 {
			return false, false, nil
		}
	}
	if count != 2 {
		return false, false, domain.ErrNotReady
	}
	// Both request admission and the first retirement take this same lock. The
	// cleanup transaction must lock actor, policy, then project and budget.
	_, err = tx.ExecContext(ctx, "INSERT INTO retention_identity_policies(workspace_id) VALUES(?) ON DUPLICATE KEY UPDATE workspace_id=VALUES(workspace_id)", workspace)
	if err != nil {
		return
	}
	err = tx.QueryRowContext(ctx, "SELECT legacy_writes_closed FROM retention_identity_policies WHERE workspace_id=? FOR UPDATE", workspace).Scan(&closed)
	return true, closed, err
}

func requestIdentityTx(ctx context.Context, tx *sql.Tx, a domain.Actor, table, key string, internal bool) (requestIdentity, error) {
	var result requestIdentity
	query := requestIdentitySQL(table)
	if query == "" || !validName(key, 120) || strings.HasPrefix(strings.ToLower(key), "retired-v1:") {
		return result, domain.ErrValidation
	}
	enabled, closed, err := requestIdentityPolicyTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return result, err
	}
	if enabled {
		var alias requestIdentity
		var workspace, ownerKey string
		err = tx.QueryRowContext(ctx, "SELECT workspace_id,original_owner_key,anonymous_key,source_id,request_hash,operation_kind FROM retention_operation_keys WHERE actor_id=? AND owner_table=? AND original_key_hash=? FOR UPDATE", a.ID, table, digest(key)).Scan(&workspace, &ownerKey, &alias.Key, &alias.ID, &alias.Hash, &alias.Kind)
		if err == nil {
			primary := map[string]string{"id": alias.ID}
			if table == "operations" {
				primary = map[string]string{"actor_id": a.ID, "operation_id": key}
			}
			expectedOwner, keyErr := retention.RowOwnerKey(table, primary)
			if !closed || workspace != a.WorkspaceID || alias.Key != "retired-v1:"+digest(key) || keyErr != nil || ownerKey != expectedOwner {
				return result, domain.ErrNotReady
			}
			err = tx.QueryRowContext(ctx, query, a.WorkspaceID, a.ID, alias.Key).Scan(&result.Key, &result.ID, &result.Hash, &result.Kind)
			if err != nil || result != alias {
				return requestIdentity{}, domain.ErrNotReady
			}
			return result, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
	}
	err = tx.QueryRowContext(ctx, query, a.WorkspaceID, a.ID, key).Scan(&result.Key, &result.ID, &result.Hash, &result.Kind)
	if err == nil && strings.HasPrefix(result.Key, "retired-v1:") {
		// ai_ci source columns can equate accented or case-varied spellings.
		// Anonymous rows are reachable only through a verified exact alias.
		return requestIdentity{}, domain.ErrValidation
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if closed && !canonicalRequestKey(key) && !(internal && internalRequestKey(key)) {
		return result, domain.ErrConflict
	}
	return result, sql.ErrNoRows
}

func operationReceiptTx(ctx context.Context, tx *sql.Tx, a domain.Actor, key string) (hash, id string, err error) {
	return evaluationOperationReceiptTx(ctx, tx, a, key, false)
}

func evaluationOperationReceiptTx(ctx context.Context, tx *sql.Tx, a domain.Actor, key string, internal bool) (hash, id string, err error) {
	record, err := requestIdentityTx(ctx, tx, a, "operations", key, internal)
	return record.Hash, record.ID, err
}

func requestReceiptTx(ctx context.Context, tx *sql.Tx, a domain.Actor, table, key string, internal bool) (id, hash string, err error) {
	record, err := requestIdentityTx(ctx, tx, a, table, key, internal)
	return record.ID, record.Hash, err
}
