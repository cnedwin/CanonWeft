package service

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/storage"
)

// Ordinary application stores have no deletion authority. Only the explicitly
// configured retention store binds read/inventory/delete to one namespace.
type RetentionDeleteStore interface {
	RetentionScanStore
	storage.BlobGC
	RetentionNamespace() string
}

func (s *RetentionScanner) advanceObjectJob(ctx context.Context, actor, project, id string) (string, error) {
	store, ok := s.Blobs.(RetentionDeleteStore)
	if !ok {
		return "", domain.ErrNotReady
	}
	namespace := store.RetentionNamespace()
	tx, err := s.jobTransaction(ctx, actor, project)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	job, err := readRetentionJob(ctx, tx, project, id, false)
	if err != nil {
		return "", err
	}
	if job.report.RestoredDisabled {
		return "", domain.ErrConflict
	}
	if !slices.Contains([]string{"DELETING", "VERIFYING", "RETRY_PENDING"}, job.report.State) {
		return job.report.State, tx.Commit()
	}
	if job.report.State == "RETRY_PENDING" {
		if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET state='DELETING',attempt=attempt+1,reason_code='',updated_at=UTC_TIMESTAMP(6) WHERE id=?", id); err != nil {
			return "", err
		}
	}
	epoch, err := retention.BeginProjectScanTx(ctx, tx, protectedLedger(), project)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}

	tx, err = s.jobTransaction(ctx, actor, project)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	job, err = readRetentionJob(ctx, tx, project, id, false)
	if err != nil {
		return "", err
	}
	if job.report.RestoredDisabled {
		return "", domain.ErrConflict
	}
	if !slices.Contains([]string{"DELETING", "VERIFYING"}, job.report.State) {
		return job.report.State, tx.Commit()
	}
	if err := validateObjectReceiptKeys(ctx, tx, id, job.plan); err != nil {
		return "", err
	}
	// Check source receipts before touching the filesystem. A changed source or
	// restored/revoked owner can never inherit authority from an old claim.
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return "", err
	}
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil {
		return "", err
	}
	if _, done, err := nextCleanupSource(ctx, tx, project, id, job.plan, at); err != nil || !done {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_CHECKPOINT_CHANGED")
	}
	claim, err := retention.LoadObjectDeleteClaimTx(ctx, tx, project, id, namespace)
	if err != nil {
		return "", err
	}
	if claim != nil {
		scan, claim, err := retention.ScanClaimedObjectTx(ctx, tx, protectedLedger(), project, id, namespace, epoch, inventorySource{store})
		if err != nil {
			return "", err
		}
		present := false
		for _, object := range scan.Inventory.Objects {
			if object.Hash == claim.Hash {
				present = object.State == retention.ObjectPresent
			}
		}
		live := false
		for _, reference := range scan.Inventory.References {
			if reference.ObjectHash == claim.Hash && reference.State != retention.Expired {
				live = true
			}
		}
		if live || (present && !slices.Contains(scan.Plan.DeleteObjects, claim.Hash)) {
			if err := retention.ReleaseObjectDeleteTx(ctx, tx, project, id, namespace, inventorySource{store}); err != nil {
				return "", err
			}
			return setCleanupStep(ctx, tx, id, "BLOCKED", "OBJECT_NO_LONGER_ELIGIBLE")
		}
		result := "ABSENT"
		if present {
			removed, err := store.DeleteObject(ctx, project, claim.Hash)
			if err != nil {
				return "", err
			}
			switch removed {
			case storage.GCRemoved:
				result = "SUCCEEDED"
			case storage.GCAbsent:
				result = "ABSENT"
			default:
				return "", domain.ErrNotReady
			}
		}
		// This rescans actual storage. A GC success value alone is insufficient.
		if err := retention.FinishObjectDeleteTx(ctx, tx, project, id, namespace, result, inventorySource{store}); err != nil {
			return "", err
		}
		return "DELETING", tx.Commit()
	}
	scan, err := retention.CompleteProjectScanTx(ctx, tx, protectedLedger(), project, epoch, inventorySource{store})
	if err != nil {
		return "", err
	}
	for _, hash := range job.plan.DeleteObjects {
		done, err := retention.ObjectDeletionVerifiedTx(ctx, tx, project, id, namespace, hash, scan.Inventory.CapturedAt)
		if err != nil {
			return setCleanupStep(ctx, tx, id, "BLOCKED", "OBJECT_RECEIPT_INVALID")
		}
		if done {
			continue
		}
		if !slices.Contains(scan.Plan.DeleteObjects, hash) {
			return setCleanupStep(ctx, tx, id, "BLOCKED", "OBJECT_NO_LONGER_ELIGIBLE")
		}
		if err := retention.PrepareObjectDeleteTx(ctx, tx, project, id, namespace, hash, scan); err != nil {
			return "", err
		}
		return "DELETING", tx.Commit()
	}
	if len(job.plan.Blocked) > 0 {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "PLAN_HAS_BLOCKED_ITEMS")
	}
	if job.report.State != "VERIFYING" {
		return setCleanupStep(ctx, tx, id, "VERIFYING", "")
	}
	return setCleanupStep(ctx, tx, id, "COMPLETE", "")
}

func validateObjectReceiptKeys(ctx context.Context, tx *sql.Tx, id string, plan retention.Plan) error {
	allowed := make(map[string]bool, len(plan.DeleteObjects))
	for _, hash := range plan.DeleteObjects {
		allowed["object:"+hash] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT item_key,phase FROM retention_receipts WHERE job_id=? AND item_key LIKE 'object:%' FOR UPDATE", id)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var key, phase string
		if err := rows.Scan(&key, &phase); err != nil {
			return err
		}
		count++
		if !allowed[key] || strings.TrimSpace(key) != key || (phase != "REMOVED" && phase != "VERIFIED") || count > 2*len(allowed) {
			return retention.ErrInvalidInventory
		}
	}
	return rows.Err()
}
