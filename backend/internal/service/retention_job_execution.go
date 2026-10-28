package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// StartJob records explicit confirmation of the exact saved preview. The durable
// dispatcher discovers this state after commit, including after process restart.
// Saving a preview never starts execution.
func (s *RetentionScanner) StartJob(ctx context.Context, actor, project, id, hash string) (RetentionJobReport, error) {
	return s.startJob(ctx, actor, project, id, hash, false)
}

func (s *RetentionScanner) startJob(ctx context.Context, actor, project, id, hash string, automatic bool) (RetentionJobReport, error) {
	if !lowerHex32(project) || !lowerHex32(id) {
		return RetentionJobReport{}, domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.jobTransaction(ctx, actor, project)
	if err != nil {
		return RetentionJobReport{}, err
	}
	defer tx.Rollback()
	job, err := readRetentionJob(ctx, tx, project, id, false)
	if err != nil {
		return RetentionJobReport{}, err
	}
	if automatic {
		if !automaticCleanupAllowed(actor) || !job.report.Automatic || job.report.RestoredDisabled {
			return RetentionJobReport{}, domain.ErrForbidden
		}
		if job.report.State != "PLANNED" || job.report.Blocked > 0 || job.report.Overdue {
			return job.report, tx.Commit()
		}
		var operation string
		if err := tx.QueryRowContext(ctx, "SELECT operation_id FROM retention_jobs WHERE id=?", id).Scan(&operation); err != nil {
			return RetentionJobReport{}, err
		}
		if err := automaticCleanupGuardTx(ctx, tx, actor, project, operation); err != nil {
			return RetentionJobReport{}, err
		}
	}
	if job.report.RestoredDisabled || job.report.PlanHash != hash || job.report.State == "CANCELLED" {
		return RetentionJobReport{}, domain.ErrConflict
	}
	if job.report.State == "PLANNED" && !job.report.CheckpointCurrent {
		return RetentionJobReport{}, domain.ErrConflict
	}
	if slices.Contains([]string{"PLANNED", "RETRY_PENDING", "BLOCKED", "OVERDUE"}, job.report.State) {
		next := "DETACHING"
		var objectReceipts int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key LIKE 'object:%'", id).Scan(&objectReceipts); err != nil {
			return RetentionJobReport{}, err
		}
		if len(job.plan.DeleteObjects) > 0 && objectReceipts > 0 {
			next = "DELETING"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET state=?,attempt=attempt+1,reason_code='',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=?", next, id, project); err != nil {
			return RetentionJobReport{}, err
		}
		job, err = readRetentionJob(ctx, tx, project, id, false)
		if err != nil {
			return RetentionJobReport{}, err
		}
	}
	return job.report, tx.Commit()
}

// Worker-only status bookkeeping after authorization is lost. This can only
// hold an active job; it grants no authority to inspect, delete or restart it.
func (s *RetentionScanner) HoldUnavailableCleanup(ctx context.Context, id string) error {
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil || !lowerHex32(id) {
		return domain.ErrNotReady
	}
	_, err := s.Workspace.DB.ExecContext(ctx, "UPDATE retention_jobs SET state='BLOCKED',reason_code='EXECUTION_UNAVAILABLE',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND restored_disabled=0 AND state IN ('DETACHING','DELETING','VERIFYING','RETRY_PENDING')", id)
	return err
}

// AdvanceJob performs one persisted source or object step. Temporal receives only
// the opaque job ID; source text and proofs remain in the SQL transaction.
func (s *RetentionScanner) AdvanceJob(ctx context.Context, id string) (string, error) {
	if s == nil || s.Workspace == nil || s.Workspace.DB == nil || s.Blobs == nil {
		return "", domain.ErrNotReady
	}
	if !lowerHex32(id) {
		return "", domain.ErrValidation
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	var actor, project string
	if err := s.Workspace.DB.QueryRowContext(ctx, "SELECT COALESCE(actor_id,''),project_id FROM retention_jobs WHERE id=?", id).Scan(&actor, &project); err != nil {
		return "", domain.ErrNotReady
	}
	state, err := s.advanceJob(ctx, actor, project, id)
	if err == nil {
		return state, nil
	}
	// Failed SQL/proof writes are rolled back before this independent status
	// transaction. Its reason is closed and contains no private error text.
	recovery, cancelRecovery := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRecovery()
	tx, statusErr := s.jobTransaction(recovery, actor, project)
	if statusErr == nil {
		defer tx.Rollback()
		var result sql.Result
		result, statusErr = tx.ExecContext(recovery, "UPDATE retention_jobs SET reason_code=CASE WHEN state='DELETING' OR reason_code='OBJECT_STEP_FAILED' THEN 'OBJECT_STEP_FAILED' ELSE 'SOURCE_STEP_FAILED' END,state='RETRY_PENDING',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=? AND restored_disabled=FALSE AND state IN ('DETACHING','DELETING','VERIFYING','RETRY_PENDING')", id, project)
		if statusErr == nil {
			count, countErr := result.RowsAffected()
			if countErr != nil || count != 1 {
				statusErr = domain.ErrConflict
			}
		}
		if statusErr == nil {
			statusErr = tx.Commit()
		}
	}
	// No raw SQL/provider/source error may enter a future workflow history.
	publicErr := domain.ErrNotReady
	if errors.Is(err, domain.ErrForbidden) {
		publicErr = domain.ErrForbidden
	}
	if errors.Is(err, domain.ErrConflict) {
		publicErr = domain.ErrConflict
	}
	if statusErr != nil {
		return "", publicErr
	}
	return "RETRY_PENDING", publicErr
}

func (s *RetentionScanner) advanceJob(ctx context.Context, actor, project, id string) (string, error) {
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
	var reason string
	if err := tx.QueryRowContext(ctx, "SELECT reason_code FROM retention_jobs WHERE id=?", id).Scan(&reason); err != nil {
		return "", err
	}
	if job.report.State == "DELETING" || (len(job.plan.DeleteObjects) > 0 && job.report.State == "VERIFYING") || (job.report.State == "RETRY_PENDING" && reason == "OBJECT_STEP_FAILED") {
		if err := tx.Rollback(); err != nil {
			return "", err
		}
		return s.advanceObjectJob(ctx, actor, project, id)
	}
	if !slices.Contains([]string{"DETACHING", "VERIFYING", "RETRY_PENDING"}, job.report.State) {
		return job.report.State, tx.Commit()
	}
	if job.report.State == "RETRY_PENDING" {
		if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET state='DETACHING',attempt=attempt+1,reason_code='',updated_at=UTC_TIMESTAMP(6) WHERE id=?", id); err != nil {
			return "", err
		}
	}
	epoch, err := retention.BeginProjectScanTx(ctx, tx, protectedLedger(), project)
	if err != nil {
		return "", domain.ErrNotReady
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
	if !slices.Contains([]string{"DETACHING", "VERIFYING"}, job.report.State) {
		return job.report.State, tx.Commit()
	}
	scan, err := retention.CompleteProjectScanTx(ctx, tx, protectedLedger(), project, epoch, inventorySource{s.Blobs})
	if err != nil {
		return "", domain.ErrNotReady
	}
	next, done, err := nextCleanupSource(ctx, tx, project, id, job.plan, scan.Inventory.CapturedAt)
	if err != nil {
		if errors.Is(err, retention.ErrLiveExpiryConsumers) {
			return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_HAS_LIVE_DEPENDENTS")
		}
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_CHECKPOINT_CHANGED")
	}
	if done {
		if len(job.plan.DeleteObjects) > 0 {
			return setCleanupStep(ctx, tx, id, "DELETING", "")
		}
		if len(job.plan.Blocked) > 0 {
			return setCleanupStep(ctx, tx, id, "BLOCKED", "PLAN_HAS_BLOCKED_ITEMS")
		}
		if job.report.State != "VERIFYING" {
			return setCleanupStep(ctx, tx, id, "VERIFYING", "")
		}
		return setCleanupStep(ctx, tx, id, "COMPLETE", "")
	}
	if job.report.State != "DETACHING" {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "INCOMPLETE_SOURCE_RECEIPTS")
	}
	if retention.IsRequestKeyExpiry(*next) {
		input, err := retention.LoadJobRequestKeyTx(ctx, tx, project, *next, scan.Inventory.CapturedAt)
		if err != nil {
			return "", err
		}
		// Request retirement must lock actor and admission policy before the
		// project. Discard this scan and reacquire in that order, never upgrade
		// to actor/policy locks while retaining the current project lock.
		if err := tx.Rollback(); err != nil {
			return "", err
		}
		return s.advanceRequestKeyJob(ctx, actor, project, id, *next, input)
	}
	batch, err := retention.LoadExpiryOwnerBatchTx(ctx, tx, project, *next, job.plan.Expire, scan.Inventory.CapturedAt)
	if errors.Is(err, retention.ErrUnsupportedExpiryOwner) {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_HANDLER_REQUIRED")
	}
	if err != nil {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_VERSION_CHANGED")
	}
	fresh := map[string]retention.Expiry{}
	for _, item := range scan.Plan.Expire {
		fresh[item.ReferenceID] = item
	}
	for _, item := range batch.Items {
		actual, exists := fresh[item.ReferenceID]
		if !exists || actual.Rule != item.Rule || actual.PayloadHash != item.PayloadHash || actual.Revision != item.Revision || actual.Storage != item.Storage || !actual.Deadline.Equal(item.Deadline) {
			return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_VERSION_CHANGED")
		}
	}
	proofs, err := prepareCleanupBatch(batch, project, scan.Inventory.CapturedAt)
	if err != nil {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_HANDLER_REQUIRED")
	}
	if err := retention.ApplyJobDetachTx(ctx, tx, protectedLedger(), project, id, scan.Inventory.Revision, batch, proofs); err != nil {
		return "", err
	}
	return "DETACHING", tx.Commit()
}

func setCleanupStep(ctx context.Context, tx *sql.Tx, id, state, reason string) (string, error) {
	if _, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET state=?,reason_code=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", state, reason, id); err != nil {
		return "", err
	}
	return state, tx.Commit()
}

func nextCleanupSource(ctx context.Context, tx *sql.Tx, project, id string, plan retention.Plan, at time.Time) (*retention.Expiry, bool, error) {
	waitingForConsumers := false
	for i, item := range plan.Expire {
		done, err := retention.JobItemDetachedTx(ctx, tx, project, id, item, at)
		if err != nil {
			return nil, false, err
		}
		if !done {
			live, err := retention.ExpiryHasLiveConsumersTx(ctx, tx, project, item, plan.Expire, at)
			if err != nil {
				return nil, false, err
			}
			if live {
				waitingForConsumers = true
				continue
			}
			return &plan.Expire[i], false, nil
		}
	}
	if waitingForConsumers {
		return nil, false, retention.ErrLiveExpiryConsumers
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key NOT LIKE 'object:%'", id).Scan(&count); err != nil {
		return nil, false, err
	}
	if count != 2*len(plan.Expire) {
		return nil, false, retention.ErrInvalidInventory
	}
	return nil, true, nil
}

func prepareCleanupBatch(batch retention.ExpiryOwnerBatch, project string, at time.Time) ([]retention.ExpiredProjection, error) {
	row, err := batch.Index.Record(batch.Source.Table, batch.Source.Primary)
	if err != nil {
		return nil, err
	}
	proofs := map[string]retention.ExpiredProjection{}
	var structured []retention.Expiry
	for _, item := range batch.Items {
		if len(retention.TransientColumns(item.Rule)) > 0 {
			field := strings.TrimPrefix(item.Rule, batch.Source.Table+".")
			summary, err := retention.TransientEvidence(row, field)
			if err != nil {
				return nil, err
			}
			proof, err := retention.ProjectExpired(item, []byte(row.Values[field]), summary, at)
			if retention.AnonymizedLabelAllowed(item.Rule) {
				proof, err = retention.ProjectAnonymizedLabel(item, []byte(row.Values[field]), summary, at)
			}
			if err != nil {
				return nil, err
			}
			proofs[item.ReferenceID] = proof
			continue
		}
		if retention.DirectExecutionObjectRule(item.Rule) || retention.DirectImportObjectRule(item.Rule) || retention.DirectKnowledgeObjectRule(item.Rule) || retention.DirectPublicationObjectRule(item.Rule) || retention.DirectSkillObjectRule(item.Rule) {
			field := strings.TrimPrefix(item.Rule, batch.Source.Table+".")
			value, exists := row.Values[field]
			if item.Rule == "skill_packages.source.archive_hash" {
				value, err = retention.SkillArchiveSourceValue(row)
				exists = err == nil
			}
			if strings.HasPrefix(field, "manifest.artifacts.") {
				value, err = retention.PublicationArtifactSourceValue(row, field)
				exists = err == nil
			}
			if !exists || row.Nulls[field] {
				return nil, retention.ErrInvalidInventory
			}
			proof, err := retention.ProjectExpired(item, []byte(value), retention.EvidenceSummary{ValueHash: value}, at)
			if err != nil {
				return nil, err
			}
			proofs[item.ReferenceID] = proof
			continue
		}
		if batch.Source.Table == "recipe_runs" || batch.Source.Table == "recipe_steps" {
			field := strings.TrimPrefix(item.Rule, batch.Source.Table+".")
			if field == "frozen" {
				value := row.Values[field]
				summary, err := retention.RecipeEvidence(batch.Source.Table, row.Values["id"], row.Values["run_id"], []byte(value), row.Values["frozen_hash"])
				if err != nil {
					return nil, err
				}
				proof, err := retention.ProjectExpired(item, []byte(value), summary, at)
				if err != nil {
					return nil, err
				}
				proofs[item.ReferenceID] = proof
				continue
			}
		}
		if item.Rule == "context_packages.payload" {
			summary, err := retention.ContextEvidence([]byte(row.Values["payload"]), row.Values["content_hash"])
			if err != nil || summary.Context.ID != row.Values["id"] || summary.Context.IndexID != row.Values["index_id"] {
				return nil, retention.ErrInvalidInventory
			}
			proof, err := retention.ProjectExpired(item, []byte(row.Values["payload"]), summary, at)
			if err != nil {
				return nil, err
			}
			proofs[item.ReferenceID] = proof
			continue
		}
		if !retention.ClearScalarAllowed(item.Rule) {
			structured = append(structured, item)
			continue
		}
		field := strings.TrimPrefix(item.Rule, batch.Source.Table+".")
		value, exists := row.Values[field]
		if !exists || row.Nulls[field] {
			return nil, retention.ErrInvalidInventory
		}
		proof, err := retention.ProjectClearedScalar(item, []byte(value), retention.EvidenceSummary{}, at)
		if err != nil {
			return nil, err
		}
		proofs[item.ReferenceID] = proof
	}
	if len(structured) > 0 {
		prepared, err := prepareIndexedRetentionOwner(batch.Index, project, batch.Source.Table, batch.Source.Primary, structured, at)
		if err != nil {
			return nil, err
		}
		for _, proof := range prepared.Evidence {
			proofs[proof.ReferenceID] = proof
		}
	}
	result := make([]retention.ExpiredProjection, 0, len(batch.Items))
	for _, item := range batch.Items {
		proof, ok := proofs[item.ReferenceID]
		if !ok {
			return nil, retention.ErrInvalidInventory
		}
		result = append(result, proof)
	}
	return result, nil
}
