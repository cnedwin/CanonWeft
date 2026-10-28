package retention

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/authority"
)

type ObjectDeleteClaim struct {
	Schema      int       `json:"schema"`
	Project     string    `json:"project"`
	Job         string    `json:"job"`
	Plan        string    `json:"plan"`
	Namespace   string    `json:"namespace"`
	Hash        string    `json:"hash"`
	Revision    int64     `json:"revision"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LeaseUntil  time.Time `json:"lease_until"`
}

func readCleanupObject(ctx context.Context, tx *sql.Tx, project, hash string) (Object, time.Time, error) {
	var object Object
	var first string
	var lease, deleted sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT object_hash,state,revision,CAST(first_seen_at AS CHAR),CAST(lease_until AS CHAR),CAST(deleted_at AS CHAR) FROM retention_objects WHERE project_id=? AND object_hash=? FOR UPDATE", project, hash).Scan(&object.Hash, &object.State, &object.Revision, &first, &lease, &deleted)
	if err != nil {
		return object, time.Time{}, err
	}
	object.FirstSeenAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", first, time.UTC)
	if err != nil || object.Revision < 1 || !hash64.MatchString(object.Hash) {
		return Object{}, time.Time{}, ErrInvalidInventory
	}
	if lease.Valid {
		object.LeaseUntil, err = time.ParseInLocation("2006-01-02 15:04:05.999999", lease.String, time.UTC)
		if err != nil {
			return Object{}, time.Time{}, err
		}
	}
	var deletedAt time.Time
	if deleted.Valid {
		deletedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", deleted.String, time.UTC)
	}
	return object, deletedAt, err
}

func objectClaim(project, job, namespace string, plan Plan, object Object) ObjectDeleteClaim {
	return ObjectDeleteClaim{Schema: 1, Project: project, Job: job, Plan: plan.Hash, Namespace: namespace, Hash: object.Hash, Revision: object.Revision, FirstSeenAt: object.FirstSeenAt, LeaseUntil: object.LeaseUntil}
}

func cleanupObjectPlan(ctx context.Context, tx *sql.Tx, project, job string) (Plan, error) {
	var raw, state, hash string
	var disabled bool
	if err := tx.QueryRowContext(ctx, "SELECT plan,state,plan_hash,restored_disabled FROM retention_jobs WHERE id=? AND project_id=? FOR UPDATE", job, project).Scan(&raw, &state, &hash, &disabled); err != nil {
		return Plan{}, err
	}
	plan, err := ValidatePlanRecord(raw)
	if err != nil || plan.ProjectID != project || plan.Hash != hash || disabled || !slices.Contains([]string{"DELETING", "VERIFYING", "RETRY_PENDING", "BLOCKED"}, state) {
		return Plan{}, ErrInvalidInventory
	}
	return plan, nil
}

// A BLOCKED REMOVED receipt plus DELETING metadata is a prepared attempt, never
// evidence of erasure. Its digest binds this job/plan, storage namespace, original
// age, lease and exact object revision. Only one live claim may own an object.
func LoadObjectDeleteClaimTx(ctx context.Context, tx *sql.Tx, project, job, namespace string) (*ObjectDeleteClaim, error) {
	if !hash64.MatchString(namespace) {
		return nil, ErrInvalidInventory
	}
	plan, err := cleanupObjectPlan(ctx, tx, project, job)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT r.item_key,r.evidence_hash FROM retention_receipts r JOIN retention_objects o ON o.project_id=? AND r.item_key=CONCAT('object:',o.object_hash) WHERE r.job_id=? AND r.phase='REMOVED' AND r.result='BLOCKED' AND o.state='DELETING' FOR UPDATE", project, job)
	if err != nil {
		return nil, err
	}
	var key, evidence string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&key, &evidence); err != nil {
			rows.Close()
			return nil, err
		}
		count++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if count == 0 {
		return nil, nil
	}
	if count != 1 {
		return nil, ErrInvalidInventory
	}
	hash := strings.TrimPrefix(key, "object:")
	if !slices.Contains(plan.DeleteObjects, hash) {
		return nil, ErrInvalidInventory
	}
	object, deleted, err := readCleanupObject(ctx, tx, project, hash)
	if err != nil || object.State != "DELETING" || object.Revision < 2 || !deleted.IsZero() {
		return nil, ErrInvalidInventory
	}
	claim := objectClaim(project, job, namespace, plan, object)
	if stableHash(claim) != evidence {
		return nil, ErrInvalidInventory
	}
	var cursor string
	if err := tx.QueryRowContext(ctx, "SELECT cursor_key FROM retention_jobs WHERE id=?", job).Scan(&cursor); err != nil || cursor != key {
		return nil, ErrInvalidInventory
	}
	var owners, verified int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts r JOIN retention_jobs j ON j.id=r.job_id WHERE j.project_id=? AND r.item_key=? AND j.cursor_key=r.item_key AND r.phase='REMOVED' AND r.result='BLOCKED' AND j.state IN ('DELETING','VERIFYING','RETRY_PENDING','BLOCKED')", project, key).Scan(&owners); err != nil || owners != 1 {
		return nil, ErrInvalidInventory
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key=? AND phase='VERIFIED'", job, key).Scan(&verified); err != nil || verified != 0 {
		return nil, ErrInvalidInventory
	}
	var validClock bool
	if err := tx.QueryRowContext(ctx, "SELECT r.recorded_at>=j.created_at AND r.recorded_at<=j.updated_at AND j.updated_at<=UTC_TIMESTAMP(6) FROM retention_receipts r JOIN retention_jobs j ON j.id=r.job_id WHERE r.job_id=? AND r.item_key=? AND r.phase='REMOVED'", job, key).Scan(&validClock); err != nil || !validClock {
		return nil, ErrInvalidInventory
	}
	return &claim, nil
}

func objectHasLiveReference(ctx context.Context, tx *sql.Tx, project, hash string) (bool, error) {
	var live int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_references WHERE project_id=? AND object_hash=? AND state<>'EXPIRED'", project, hash).Scan(&live)
	return live != 0, err
}

func PrepareObjectDeleteTx(ctx context.Context, tx *sql.Tx, project, job, namespace, hash string, scan ProjectScan) error {
	plan, err := cleanupObjectPlan(ctx, tx, project, job)
	if err != nil || !hash64.MatchString(namespace) || !slices.Contains(plan.DeleteObjects, hash) || !slices.Contains(scan.Plan.DeleteObjects, hash) {
		return ErrInvalidInventory
	}
	verified, err := BuildPlan(scan.Inventory, scan.Plan.Cutoff)
	if err != nil || verified.Hash != scan.Plan.Hash || scan.Inventory.ProjectID != project {
		return ErrInvalidInventory
	}
	var state string
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT state,inventory_revision FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&state, &revision); err != nil || state != "CURRENT" || revision != scan.Inventory.Revision {
		return ErrInvalidInventory
	}
	object, deleted, err := readCleanupObject(ctx, tx, project, hash)
	if err != nil || object.State != ObjectPresent || !deleted.IsZero() || object.Revision == math.MaxInt64 || object.LeaseUntil.After(scan.Inventory.CapturedAt) {
		return ErrInvalidInventory
	}
	matched := false
	for _, observed := range scan.Inventory.Objects {
		if observed.Hash == hash {
			matched = observed.State == object.State && observed.Revision == object.Revision && observed.FirstSeenAt.Equal(object.FirstSeenAt) && observed.LeaseUntil.Equal(object.LeaseUntil)
		}
	}
	if !matched {
		return ErrInvalidInventory
	}
	live, err := objectHasLiveReference(ctx, tx, project, hash)
	if err != nil || live {
		return ErrInvalidInventory
	}
	object.Revision++
	claim := objectClaim(project, job, namespace, plan, object)
	var successful int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key=? AND (phase<>'REMOVED' OR result<>'BLOCKED')", job, "object:"+hash).Scan(&successful); err != nil || successful != 0 {
		return ErrInvalidInventory
	}
	if err := updateCleanupObject(ctx, tx, "UPDATE retention_objects SET state='DELETING',revision=? WHERE project_id=? AND object_hash=? AND state='PRESENT'", object.Revision, project, hash); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO retention_receipts(job_id,item_key,phase,result,evidence_hash,recorded_at) VALUES(?,?,'REMOVED','BLOCKED',?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE evidence_hash=VALUES(evidence_hash),recorded_at=VALUES(recorded_at)", job, "object:"+hash, stableHash(claim)); err != nil {
		return err
	}
	if err := invalidateCapturedInventory(ctx, tx, project); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE retention_jobs SET state='DELETING',cursor_key=?,reason_code='OBJECT_DELETE_PREPARED',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=?", "object:"+hash, job, project)
	return err
}

// The normal scanner rejects DELETING/missing PRESENT files. Only a verified
// persisted claim can project its one DELETING object to observed present/absent
// for this locked transaction. Callers must finish/release or roll back, never
// commit the projected CURRENT checkpoint by itself.
func ScanClaimedObjectTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project, job, namespace string, epoch int64, store InventoryStore) (ProjectScan, *ObjectDeleteClaim, error) {
	claim, err := LoadObjectDeleteClaimTx(ctx, tx, project, job, namespace)
	if err != nil || claim == nil {
		return ProjectScan{}, nil, ErrInvalidInventory
	}
	scan, err := completeProjectScanTx(ctx, tx, ledger, project, epoch, store, claim)
	return scan, claim, err
}

func ReleaseObjectDeleteTx(ctx context.Context, tx *sql.Tx, project, job, namespace string, store InventoryStore) error {
	claim, err := LoadObjectDeleteClaimTx(ctx, tx, project, job, namespace)
	if err != nil || claim == nil {
		return ErrInvalidInventory
	}
	objects, err := store.InventoryObjects(ctx, project)
	if err != nil {
		return err
	}
	present := false
	for _, object := range objects {
		present = present || object.Hash == claim.Hash
	}
	if !present || claim.Revision == math.MaxInt64 {
		return ErrInvalidInventory
	}
	if err := updateCleanupObject(ctx, tx, "UPDATE retention_objects SET state='PRESENT',revision=revision+1 WHERE project_id=? AND object_hash=? AND state='DELETING' AND revision=?", project, claim.Hash, claim.Revision); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET cursor_key='',updated_at=UTC_TIMESTAMP(6) WHERE id=?", job); err != nil {
		return err
	}
	return invalidateCapturedInventory(ctx, tx, project)
}

func FinishObjectDeleteTx(ctx context.Context, tx *sql.Tx, project, job, namespace, result string, store InventoryStore) error {
	if result != "SUCCEEDED" && result != "ABSENT" {
		return ErrInvalidInventory
	}
	claim, err := LoadObjectDeleteClaimTx(ctx, tx, project, job, namespace)
	if err != nil || claim == nil || claim.Revision == math.MaxInt64 {
		return ErrInvalidInventory
	}
	objects, err := store.InventoryObjects(ctx, project)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if object.Hash == claim.Hash {
			return ErrInvalidInventory
		}
	}
	live, err := objectHasLiveReference(ctx, tx, project, claim.Hash)
	if err != nil || live {
		return ErrInvalidInventory
	}
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return err
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil || claim.LeaseUntil.After(at) || claim.FirstSeenAt.After(at) {
		return ErrInvalidInventory
	}
	if err := updateCleanupObject(ctx, tx, "UPDATE retention_objects SET state='DELETED',deleted_at=?,revision=revision+1 WHERE project_id=? AND object_hash=? AND state='DELETING' AND revision=?", clock, project, claim.Hash, claim.Revision); err != nil {
		return err
	}
	if err := updateCleanupObject(ctx, tx, "UPDATE retention_receipts SET result=?,recorded_at=? WHERE job_id=? AND item_key=? AND phase='REMOVED' AND result='BLOCKED' AND evidence_hash=?", result, clock, job, "object:"+claim.Hash, stableHash(*claim)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO retention_receipts(job_id,item_key,phase,result,evidence_hash,recorded_at) VALUES(?,?,'VERIFIED','ABSENT',?,?)", job, "object:"+claim.Hash, stableHash(*claim), clock); err != nil {
		return err
	}
	if err := invalidateCapturedInventory(ctx, tx, project); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE retention_jobs SET state='DELETING',cursor_key='',reason_code='',updated_at=? WHERE id=? AND project_id=?", clock, job, project)
	return err
}

func ObjectDeletionVerifiedTx(ctx context.Context, tx *sql.Tx, project, job, namespace, hash string, at time.Time) (bool, error) {
	plan, err := cleanupObjectPlan(ctx, tx, project, job)
	if err != nil {
		return false, err
	}
	return objectDeletionReceiptsVerifiedTx(ctx, tx, project, job, namespace, hash, at, plan)
}

func objectDeletionReceiptsVerifiedTx(ctx context.Context, tx *sql.Tx, project, job, namespace, hash string, at time.Time, plan Plan) (bool, error) {
	if !hash64.MatchString(namespace) || !slices.Contains(plan.DeleteObjects, hash) {
		return false, ErrInvalidInventory
	}
	rows, err := tx.QueryContext(ctx, "SELECT r.phase,r.result,r.evidence_hash,CAST(r.recorded_at AS CHAR),CAST(j.created_at AS CHAR),CAST(j.updated_at AS CHAR) FROM retention_receipts r JOIN retention_jobs j ON j.id=r.job_id WHERE r.job_id=? AND r.item_key=? FOR UPDATE", job, "object:"+hash)
	if err != nil {
		return false, err
	}
	type receipt struct {
		result, hash string
		at           time.Time
	}
	receipts := map[string]receipt{}
	for rows.Next() {
		var phase, result, hash, clock, created, updated string
		if err := rows.Scan(&phase, &result, &hash, &clock, &created, &updated); err != nil {
			rows.Close()
			return false, err
		}
		when, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
		createdAt, createdErr := time.ParseInLocation("2006-01-02 15:04:05.999999", created, time.UTC)
		updatedAt, updatedErr := time.ParseInLocation("2006-01-02 15:04:05.999999", updated, time.UTC)
		if err != nil || createdErr != nil || updatedErr != nil || when.Before(createdAt) || when.After(updatedAt) || updatedAt.After(at) {
			rows.Close()
			return false, ErrInvalidInventory
		}
		receipts[phase] = receipt{result, hash, when}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(receipts) == 0 || (len(receipts) == 1 && receipts["REMOVED"].result == "BLOCKED") {
		return false, nil
	}
	removed, verified := receipts["REMOVED"], receipts["VERIFIED"]
	if len(receipts) != 2 || (removed.result != "SUCCEEDED" && removed.result != "ABSENT") || verified.result != "ABSENT" || removed.hash != verified.hash || verified.at.Before(removed.at) || verified.at.After(at) {
		return false, ErrInvalidInventory
	}
	object, deleted, err := readCleanupObject(ctx, tx, project, hash)
	if err != nil || object.State != ObjectDeleted || object.Revision < 3 || !deleted.Equal(verified.at) || object.LeaseUntil.After(deleted) || object.FirstSeenAt.After(deleted) {
		return false, ErrInvalidInventory
	}
	object.Revision--
	if stableHash(objectClaim(project, job, namespace, plan, object)) != removed.hash {
		return false, ErrInvalidInventory
	}
	return true, nil
}

func updateCleanupObject(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrInvalidInventory
	}
	return nil
}
