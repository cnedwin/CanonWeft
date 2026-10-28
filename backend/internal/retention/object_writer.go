package retention

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strings"
	"time"
)

// ProtectObjectWrite must share the authorized business transaction that writes
// the file and saves its reference. Callers must roll back on any error and hold
// this transaction through physical IO. A cleaner must take the same locks
// through deletion. This lease is neither authorization nor evidence of a file.
func ProtectObjectWrite(ctx context.Context, tx *sql.Tx, project, hash string) error {
	return ProtectObjectWriteInNamespace(ctx, tx, project, hash, nil)
}

// bind is invoked only for a verified tombstone, while holding the project and
// object locks. Its writer must remain bound through physical IO.
func ProtectObjectWriteInNamespace(ctx context.Context, tx *sql.Tx, project, hash string, bind func() (string, error)) error {
	if tx == nil || !id32.MatchString(project) || !hash64.MatchString(hash) {
		return ErrInvalidInventory
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return err
	}
	enabled, err := writerRegistrationEnabled(ctx, tx)
	if err != nil {
		return err
	}
	if enabled {
		if err := leaseObjectWrite(ctx, tx, project, hash, bind); err != nil {
			return err
		}
		// A physical write can add an orphan before any business reference is
		// captured. Invalidate the complete object inventory in this same
		// transaction, even when no source-column capture follows this call.
		return invalidateCapturedInventory(ctx, tx, project)
	}
	return nil
}

func writerRegistrationEnabled(ctx context.Context, tx *sql.Tx) (bool, error) {
	var installed int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE name=?", MigrationName).Scan(&installed); err != nil {
		return false, err
	}
	if installed != 0 {
		if err := verifyRegistrationCatalog(ctx, tx); err != nil {
			return false, err
		}
		return true, nil
	}
	// An incomplete installation must not silently fall back to unguarded IO.
	// These names are a fixed internal catalog, never user-provided identifiers.
	names := make([]string, 0, len(registrationMetadata()))
	for name := range registrationMetadata() {
		names = append(names, "'"+name+"'")
	}
	slices.Sort(names)
	var present int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name IN ("+strings.Join(names, ",")+")").Scan(&present)
	if err != nil || present != 0 {
		return false, ErrInvalidInventory
	}
	return false, nil
}

func leaseObjectWrite(ctx context.Context, tx *sql.Tx, project, hash string, bind func() (string, error)) error {
	var first string
	var state ObjectState
	var lease, deleted sql.NullString
	var revision int64
	err := tx.QueryRowContext(ctx, "SELECT CAST(first_seen_at AS CHAR),CAST(lease_until AS CHAR),state,revision,CAST(deleted_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=? FOR UPDATE", project, hash).Scan(&first, &lease, &state, &revision, &deleted)
	found := err != sql.ErrNoRows
	if err != nil && found {
		return err
	}
	// Take UTC after waiting for the object lock, never from the application
	// clock or the start of a potentially blocked transaction.
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return err
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil || now.IsZero() {
		return ErrInvalidInventory
	}
	until := now.Add(10 * time.Minute)
	if !found {
		// first_seen_at is an observation, not a claim about an old file's birth.
		_, err := tx.ExecContext(ctx, "INSERT INTO retention_objects(project_id,object_hash,first_seen_at,lease_until,state,revision) VALUES(?,?,?,?,'PRESENT',1)", project, hash, lifecycleSQLTime(now), lifecycleSQLTime(until))
		return err
	}
	firstSeen, err := time.ParseInLocation("2006-01-02 15:04:05.999999", first, time.UTC)
	if err != nil || firstSeen.IsZero() || firstSeen.After(now) || revision < 1 || revision == math.MaxInt64 {
		return ErrInvalidInventory
	}
	if state == ObjectDeleted {
		if bind == nil {
			return ErrInvalidInventory
		}
		namespace, err := bind()
		if err != nil {
			return err
		}
		return recreateDeletedObjectTx(ctx, tx, project, hash, namespace, now, until)
	}
	if state != ObjectPresent || deleted.Valid {
		return ErrInvalidInventory
	}
	if lease.Valid {
		previous, err := time.ParseInLocation("2006-01-02 15:04:05.999999", lease.String, time.UTC)
		if err != nil || previous.IsZero() {
			return ErrInvalidInventory
		}
		if !previous.Before(until) {
			return nil
		}
	}
	result, err := tx.ExecContext(ctx, "UPDATE retention_objects SET lease_until=?,revision=revision+1 WHERE project_id=? AND object_hash=? AND revision=? AND state='PRESENT'", lifecycleSQLTime(until), project, hash, revision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrInvalidInventory
	}
	return nil
}

// A new explicit write may replace a completed deletion, but cannot revive old
// references or race an unfinished cleanup. The immutable receipts remain history.
func recreateDeletedObjectTx(ctx context.Context, tx *sql.Tx, project, hash, namespace string, now, until time.Time) error {
	if !hash64.MatchString(namespace) {
		return ErrInvalidInventory
	}
	object, deleted, err := readCleanupObject(ctx, tx, project, hash)
	if err != nil || object.State != ObjectDeleted || object.Revision < 3 || object.Revision == math.MaxInt64 || deleted.IsZero() || deleted.After(now) || object.FirstSeenAt.After(deleted) || object.LeaseUntil.After(deleted) {
		return ErrInvalidInventory
	}
	live, err := objectHasLiveReference(ctx, tx, project, hash)
	if err != nil {
		return err
	}
	if live {
		return ErrInvalidInventory
	}
	rows, err := tx.QueryContext(ctx, "SELECT j.id,j.plan,j.plan_hash FROM retention_jobs j JOIN retention_receipts r ON r.job_id=j.id WHERE j.project_id=? AND j.state='COMPLETE' AND j.restored_disabled=0 AND r.item_key=? AND r.phase='VERIFIED' AND r.result='ABSENT' AND r.recorded_at=? LIMIT 2 FOR UPDATE", project, "object:"+hash, lifecycleSQLTime(deleted))
	if err != nil {
		return err
	}
	var job, raw, planHash string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&job, &raw, &planHash); err != nil {
			rows.Close()
			return err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrInvalidInventory
	}
	plan, err := ValidatePlanRecord(raw)
	if err != nil || plan.ProjectID != project || plan.Hash != planHash {
		return ErrInvalidInventory
	}
	verified, err := objectDeletionReceiptsVerifiedTx(ctx, tx, project, job, namespace, hash, now, plan)
	if err != nil {
		return err
	}
	if !verified {
		return ErrInvalidInventory
	}
	return updateCleanupObject(ctx, tx, "UPDATE retention_objects SET state='PRESENT',first_seen_at=?,lease_until=?,deleted_at=NULL,revision=revision+1 WHERE project_id=? AND object_hash=? AND state='DELETED' AND revision=?", lifecycleSQLTime(now), lifecycleSQLTime(until), project, hash, object.Revision)
}
