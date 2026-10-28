package retention

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"novelstudio/internal/authority"
)

// Recovery writes are recorded outside the author transaction so its rollback
// cannot erase the only route to unreferenced files. The final commit decision
// is updated inside that author transaction. No erased author text is stored.
type RecoveryAttempt struct {
	Target, Project, Actor, Namespace string
	Objects                           []PhysicalObject
	State                             string
	Disabled                          bool
	Created, Updated                  time.Time
}

func ParseRecoveryObjects(raw string) ([]PhysicalObject, error) {
	var objects []PhysicalObject
	if len(raw) > 16<<20 || json.Unmarshal([]byte(raw), &objects) != nil || objects == nil {
		return nil, ErrInvalidInventory
	}
	canonical, err := json.Marshal(objects)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		return nil, ErrInvalidInventory
	}
	return objects, nil
}

func ValidateRecoveryAttempt(a RecoveryAttempt) error {
	if !id32.MatchString(a.Target) || !id32.MatchString(a.Project) || a.Target == a.Project || !id32.MatchString(a.Actor) || !hash64.MatchString(a.Namespace) || !slices.Contains([]string{"WRITING", "COMMITTED", "CLEANED", "BLOCKED"}, a.State) || a.Created.IsZero() || a.Updated.Before(a.Created) || len(a.Objects) > 250000 {
		return ErrInvalidInventory
	}
	seen := map[string]bool{}
	var total int64
	for _, object := range a.Objects {
		if !hash64.MatchString(object.Hash) || seen[object.Hash] || object.Size < 0 || object.Size > 32<<20 {
			return ErrInvalidInventory
		}
		seen[object.Hash] = true
		total += object.Size
		if total > 512<<20 {
			return ErrInvalidInventory
		}
	}
	return nil
}

// BeginRecoveryAttempt returns false only on the intact pre-retention schema.
// The caller already holds the author/source/brand-new target project locks.
func BeginRecoveryAttempt(ctx context.Context, db *sql.DB, project, target, actor, namespace string) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil || !enabled {
		return false, err
	}
	a := RecoveryAttempt{Target: target, Project: project, Actor: actor, Namespace: namespace, State: "WRITING", Created: time.Now().UTC(), Updated: time.Now().UTC()}
	if err := ValidateRecoveryAttempt(a); err != nil {
		return false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO retention_recovery_attempts(target_project_id,project_id,actor_id,storage_namespace,objects,created_at,updated_at) VALUES(?,?,?,?,'[]',UTC_TIMESTAMP(6),UTC_TIMESTAMP(6))", target, project, actor, namespace)
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func readRecoveryAttempt(ctx context.Context, tx *sql.Tx, target string) (RecoveryAttempt, error) {
	var a RecoveryAttempt
	var raw string
	err := tx.QueryRowContext(ctx, "SELECT target_project_id,project_id,actor_id,storage_namespace,objects,state,restored_disabled,created_at,updated_at FROM retention_recovery_attempts WHERE target_project_id=? AND OCTET_LENGTH(objects)<=16777216 FOR UPDATE", target).Scan(&a.Target, &a.Project, &a.Actor, &a.Namespace, &raw, &a.State, &a.Disabled, &a.Created, &a.Updated)
	if err != nil {
		return a, err
	}
	a.Objects, err = ParseRecoveryObjects(raw)
	if err != nil || ValidateRecoveryAttempt(a) != nil {
		return a, ErrInvalidInventory
	}
	return a, nil
}

func RecordRecoveryObject(ctx context.Context, db *sql.DB, target string, object PhysicalObject) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := readRecoveryAttempt(ctx, tx, target)
	if err != nil {
		return err
	}
	if a.State != "WRITING" || a.Disabled {
		return ErrInvalidInventory
	}
	for _, known := range a.Objects {
		if known.Hash == object.Hash {
			if known.Size != object.Size {
				return ErrInvalidInventory
			}
			return nil
		}
	}
	a.Objects = append(a.Objects, object)
	if err := ValidateRecoveryAttempt(a); err != nil {
		return err
	}
	raw, _ := json.Marshal(a.Objects)
	if _, err := tx.ExecContext(ctx, "UPDATE retention_recovery_attempts SET objects=?,updated_at=UTC_TIMESTAMP(6) WHERE target_project_id=?", string(raw), target); err != nil {
		return err
	}
	return tx.Commit()
}

func CommitRecoveryAttemptTx(ctx context.Context, tx *sql.Tx, target string) error {
	result, err := tx.ExecContext(ctx, "UPDATE retention_recovery_attempts SET state='COMMITTED',updated_at=UTC_TIMESTAMP(6) WHERE target_project_id=? AND state='WRITING' AND restored_disabled=0", target)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil || n != 1 {
		return ErrInvalidInventory
	}
	return nil
}

type RecoveryStore interface {
	InventoryRecoveryObjects(context.Context, string, []PhysicalObject) ([]PhysicalObject, error)
	RemoveRecoveryObject(context.Context, string, PhysicalObject) error
	RetentionNamespace() string
}

// ReconcileRecovery checks one abandoned attempt. The actor lock serializes
// this against the live recovery transaction. Neither elapsed time nor an old
// archive alone grants deletion: the target must never have been committed.
func ReconcileRecovery(ctx context.Context, db *sql.DB, ledger authority.Ledger, store RecoveryStore) (bool, error) {
	if store == nil || !hash64.MatchString(store.RetentionNamespace()) {
		return false, ErrInvalidInventory
	}
	var target, actor string
	err := db.QueryRowContext(ctx, "SELECT target_project_id,actor_id FROM retention_recovery_attempts WHERE state='WRITING' AND restored_disabled=0 AND updated_at<UTC_TIMESTAMP(6)-INTERVAL 10 MINUTE ORDER BY updated_at,target_project_id LIMIT 1").Scan(&target, &actor)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var workspace string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM users WHERE id=? FOR UPDATE", actor).Scan(&workspace); err != nil {
		return false, err
	}
	a, err := readRecoveryAttempt(ctx, tx, target)
	if err != nil {
		if errors.Is(err, ErrInvalidInventory) {
			// Preserve damaged evidence and advance to other attempts. A bad
			// journal must never authorize IO or starve the maintenance queue.
			if _, updateErr := tx.ExecContext(ctx, "UPDATE retention_recovery_attempts SET state='BLOCKED',updated_at=UTC_TIMESTAMP(6) WHERE target_project_id=? AND actor_id=? AND state='WRITING' AND restored_disabled=0", target, actor); updateErr != nil {
				return false, updateErr
			}
			return true, tx.Commit()
		}
		return false, err
	}
	var now time.Time
	if err := tx.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&now); err != nil {
		return false, err
	}
	if a.Disabled || a.State != "WRITING" || a.Actor != actor || !a.Updated.Before(now.Add(-10*time.Minute)) {
		return false, nil
	}
	finish := func(state string) (bool, error) {
		if _, err := tx.ExecContext(ctx, "UPDATE retention_recovery_attempts SET state=?,updated_at=UTC_TIMESTAMP(6) WHERE target_project_id=?", state, target); err != nil {
			return false, err
		}
		return true, tx.Commit()
	}
	var sourceWorkspace string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM projects WHERE id=? FOR UPDATE", a.Project).Scan(&sourceWorkspace); err != nil {
		return false, err
	}
	if sourceWorkspace != workspace || a.Namespace != store.RetentionNamespace() {
		return finish("BLOCKED")
	}
	var exists string
	err = tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", target).Scan(&exists)
	if err == nil {
		return finish("BLOCKED")
	}
	if err != sql.ErrNoRows {
		return false, err
	}
	active, err := ledger.ProjectActiveTx(ctx, tx, target)
	if err != nil {
		return false, err
	}
	deleted, err := ledger.DeletionRecordedTx(ctx, tx, target)
	if err != nil {
		return false, err
	}
	if active || deleted {
		return finish("BLOCKED")
	}
	actual, err := store.InventoryRecoveryObjects(ctx, target, a.Objects)
	if err != nil {
		if errors.Is(err, ErrInvalidInventory) {
			return finish("BLOCKED")
		}
		return false, err
	}
	expected := map[string]int64{}
	for _, object := range a.Objects {
		expected[object.Hash] = object.Size
	}
	for _, object := range actual {
		size, known := expected[object.Hash]
		if !known || size != object.Size {
			return finish("BLOCKED")
		}
	}
	for _, object := range actual {
		if err := store.RemoveRecoveryObject(ctx, target, object); err != nil {
			return false, err
		}
	}
	remaining, err := store.InventoryRecoveryObjects(ctx, target, a.Objects)
	if err != nil || len(remaining) != 0 {
		return false, ErrInvalidInventory
	}
	return finish("CLEANED")
}
