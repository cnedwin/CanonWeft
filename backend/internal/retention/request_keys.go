package retention

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"
)

// RequestKeyRetirement is an exact source comparison, not expiry authority. The
// cleanup coordinator must already have proved the owner's scope, deadline,
// leases and evidence and must commit its proof writes in this SAME transaction.
// No HTTP endpoint or cleanup scheduler currently invokes this primitive.
type RequestKeyRetirement struct {
	WorkspaceID, ActorID, Table, OriginalKey      string
	SourceID, RequestHash, Kind, OriginalOwnerKey string
}

type RetiredRequestKey struct {
	OriginalOwnerKey, CurrentOwnerKey, AnonymousKey string
}

type requestKeySource struct{ key, id, hash, kind string }

// RetireRequestKeyTx preserves the source ID and request fingerprint. It never
// starts, commits or rolls back the caller's transaction. On ANY error the caller
// must roll back; it must not commit a partially prepared expiry batch.
// Lock order: actor, workspace policy, then caller's project/budget/source locks.
// Callers taking project locks first must not invoke this method.
func RetireRequestKeyTx(ctx context.Context, tx *sql.Tx, in RequestKeyRetirement) (RetiredRequestKey, error) {
	var out RetiredRequestKey
	query, update := requestKeyStatements(in.Table)
	if tx == nil || query == "" || !id32.MatchString(in.WorkspaceID) || !id32.MatchString(in.ActorID) || !id32.MatchString(in.SourceID) || !hash64.MatchString(in.RequestHash) || !utf8.ValidString(in.OriginalKey) || utf8.RuneCountInString(in.OriginalKey) == 0 || utf8.RuneCountInString(in.OriginalKey) > 120 || strings.HasPrefix(strings.ToLower(in.OriginalKey), "retired-v1:") || (in.Table != "operations" && in.Kind != "") {
		return out, ErrInvalidInventory
	}
	primary := map[string]string{"id": in.SourceID}
	if in.Table == "operations" {
		primary = map[string]string{"actor_id": in.ActorID, "operation_id": in.OriginalKey}
	}
	originalOwner, err := RowOwnerKey(in.Table, primary)
	if err != nil || originalOwner != in.OriginalOwnerKey {
		return out, ErrInvalidInventory
	}
	var workspace string
	// Disabled actors' historical rows can be cleaned; disabled actors still
	// cannot issue public requests. Actor identity and workspace cannot be guessed.
	if err = tx.QueryRowContext(ctx, "SELECT workspace_id FROM users WHERE id=? FOR UPDATE", in.ActorID).Scan(&workspace); err != nil {
		return out, err
	}
	if workspace != in.WorkspaceID {
		return out, ErrInvalidInventory
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ('retention_identity_policies','retention_operation_keys')").Scan(&count); err != nil {
		return out, err
	}
	if count != 2 {
		return out, ErrInvalidInventory
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO retention_identity_policies(workspace_id) VALUES(?) ON DUPLICATE KEY UPDATE workspace_id=VALUES(workspace_id)", workspace); err != nil {
		return out, err
	}
	var closed bool
	if err = tx.QueryRowContext(ctx, "SELECT legacy_writes_closed FROM retention_identity_policies WHERE workspace_id=? FOR UPDATE", workspace).Scan(&closed); err != nil {
		return out, err
	}
	keyHash := strings.TrimPrefix(DigestKeyPart(in.OriginalKey), "sha256:")
	anonymous := "retired-v1:" + keyHash
	currentPrimary := primary
	if in.Table == "operations" {
		currentPrimary = map[string]string{"actor_id": in.ActorID, "operation_id": anonymous}
	}
	currentOwner, err := RowOwnerKey(in.Table, currentPrimary)
	if err != nil {
		return out, err
	}
	out = RetiredRequestKey{OriginalOwnerKey: originalOwner, CurrentOwnerKey: currentOwner, AnonymousKey: anonymous}
	var aliasWorkspace, aliasOwner string
	var alias requestKeySource
	err = tx.QueryRowContext(ctx, "SELECT workspace_id,original_owner_key,anonymous_key,source_id,request_hash,operation_kind FROM retention_operation_keys WHERE actor_id=? AND owner_table=? AND original_key_hash=? FOR UPDATE", in.ActorID, in.Table, keyHash).Scan(&aliasWorkspace, &aliasOwner, &alias.key, &alias.id, &alias.hash, &alias.kind)
	read := func(key string) (requestKeySource, error) {
		var source requestKeySource
		e := tx.QueryRowContext(ctx, query, workspace, in.ActorID, key).Scan(&source.key, &source.id, &source.hash, &source.kind)
		return source, e
	}
	expected := requestKeySource{anonymous, in.SourceID, in.RequestHash, in.Kind}
	if err == nil {
		if !closed || aliasWorkspace != workspace || aliasOwner != originalOwner || alias != expected {
			return RetiredRequestKey{}, ErrInvalidInventory
		}
		source, e := read(anonymous)
		if e != nil {
			return RetiredRequestKey{}, e
		}
		if source != expected {
			return RetiredRequestKey{}, ErrInvalidInventory
		}
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RetiredRequestKey{}, err
	}
	source, err := read(in.OriginalKey)
	if err != nil {
		return RetiredRequestKey{}, err
	}
	// SQL collation equality is insufficient when hashing/removing source bytes.
	if source != (requestKeySource{in.OriginalKey, in.SourceID, in.RequestHash, in.Kind}) {
		return RetiredRequestKey{}, ErrInvalidInventory
	}
	if _, err = read(anonymous); !errors.Is(err, sql.ErrNoRows) {
		if err == nil {
			err = ErrInvalidInventory
		}
		return RetiredRequestKey{}, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO retention_operation_keys(workspace_id,actor_id,owner_table,original_key_hash,original_owner_key,anonymous_key,source_id,request_hash,operation_kind) VALUES(?,?,?,?,?,?,?,?,?)", workspace, in.ActorID, in.Table, keyHash, originalOwner, anonymous, in.SourceID, in.RequestHash, in.Kind); err != nil {
		return RetiredRequestKey{}, err
	}
	changed, err := tx.ExecContext(ctx, update, anonymous, in.ActorID, in.OriginalKey)
	if err != nil {
		return RetiredRequestKey{}, err
	}
	n, err := changed.RowsAffected()
	if err != nil {
		return RetiredRequestKey{}, err
	}
	if n != 1 {
		return RetiredRequestKey{}, ErrInvalidInventory
	}
	if !closed {
		if _, err = tx.ExecContext(ctx, "UPDATE retention_identity_policies SET legacy_writes_closed=TRUE,closed_at=UTC_TIMESTAMP(6),revision=revision+1 WHERE workspace_id=? AND legacy_writes_closed=FALSE", workspace); err != nil {
			return RetiredRequestKey{}, err
		}
	}
	return out, nil
}

// Every SQL identifier is a compile-time literal. User/provider text is bound.
func requestKeyStatements(table string) (query, update string) {
	switch table {
	case "operations":
		return "SELECT o.operation_id,o.result_id,o.request_hash,o.kind FROM operations o JOIN users u ON u.id=o.actor_id WHERE u.workspace_id=? AND o.actor_id=? AND o.operation_id=? FOR UPDATE", "UPDATE operations SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "model_calls":
		return "SELECT operation_id,id,input_hash,'' FROM model_calls WHERE workspace_id=? AND actor_id=? AND operation_id=? FOR UPDATE", "UPDATE model_calls SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "source_imports":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM source_imports s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE", "UPDATE source_imports SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "ingest_jobs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM ingest_jobs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE", "UPDATE ingest_jobs SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "generation_tasks":
		return "SELECT operation_id,id,request_hash,'' FROM generation_tasks WHERE workspace_id=? AND actor_id=? AND operation_id=? FOR UPDATE", "UPDATE generation_tasks SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "recipe_runs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM recipe_runs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE", "UPDATE recipe_runs SET operation_id=? WHERE actor_id=? AND operation_id=?"
	case "semantic_review_runs":
		return "SELECT s.operation_id,s.id,s.request_hash,'' FROM semantic_review_runs s JOIN projects p ON p.id=s.project_id WHERE p.workspace_id=? AND s.actor_id=? AND s.operation_id=? FOR UPDATE", "UPDATE semantic_review_runs SET operation_id=? WHERE actor_id=? AND operation_id=?"
	default:
		return "", ""
	}
}
