package service

import (
	"context"
	"database/sql"
	"slices"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
)

// The discovered input is a hint only; actual identity and expiry are reloaded
// after taking every participating actor lock, then policy, then project.
func (s *RetentionScanner) requestCleanupTransaction(ctx context.Context, actor, project string, input retention.RequestKeyRetirement) (*sql.Tx, error) {
	if !lowerHex32(actor) || !lowerHex32(input.ActorID) || !lowerHex32(input.WorkspaceID) {
		return nil, domain.ErrValidation
	}
	tx, err := s.Workspace.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*sql.Tx, error) { tx.Rollback(); return nil, err }
	actors := []string{actor, input.ActorID}
	slices.Sort(actors)
	actors = slices.Compact(actors)
	for _, id := range actors {
		var workspace, role string
		var enabled bool
		if err := tx.QueryRowContext(ctx, "SELECT workspace_id,role,enabled FROM users WHERE id=? FOR UPDATE", id).Scan(&workspace, &role, &enabled); err != nil {
			return fail(domain.ErrForbidden)
		}
		if workspace != input.WorkspaceID || (id == actor && (!enabled || role != "owner")) {
			return fail(domain.ErrForbidden)
		}
	}
	enabled, _, err := requestIdentityPolicyTx(ctx, tx, input.WorkspaceID)
	if err != nil || !enabled {
		return fail(domain.ErrNotReady)
	}
	if err := authorizeRetentionScan(ctx, tx, actor, project); err != nil {
		return fail(err)
	}
	if err := retention.CheckCleanupStorageTx(ctx, tx); err != nil {
		return fail(domain.ErrNotReady)
	}
	return tx, nil
}

func (s *RetentionScanner) advanceRequestKeyJob(ctx context.Context, actor, project, id string, expected retention.Expiry, hint retention.RequestKeyRetirement) (string, error) {
	tx, err := s.requestCleanupTransaction(ctx, actor, project, hint)
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
	if job.report.State != "DETACHING" {
		return job.report.State, tx.Commit()
	}
	epoch, err := retention.BeginProjectScanTx(ctx, tx, protectedLedger(), project)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	tx, err = s.requestCleanupTransaction(ctx, actor, project, hint)
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
	if job.report.State != "DETACHING" {
		return job.report.State, tx.Commit()
	}
	scan, err := retention.CompleteProjectScanTx(ctx, tx, protectedLedger(), project, epoch, inventorySource{s.Blobs})
	if err != nil {
		return "", err
	}
	next, done, err := nextCleanupSource(ctx, tx, project, id, job.plan, scan.Inventory.CapturedAt)
	if err != nil || done || next == nil || *next != expected {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_CHECKPOINT_CHANGED")
	}
	fresh := false
	for _, item := range scan.Plan.Expire {
		if item == expected {
			fresh = true
			break
		}
	}
	if !fresh {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_VERSION_CHANGED")
	}
	input, err := retention.LoadJobRequestKeyTx(ctx, tx, project, *next, scan.Inventory.CapturedAt)
	if err != nil || input != hint {
		return setCleanupStep(ctx, tx, id, "BLOCKED", "SOURCE_VERSION_CHANGED")
	}
	proof, err := retention.ProjectRetiredRequestKey(project, *next, input, scan.Inventory.CapturedAt)
	if err != nil {
		return "", err
	}
	if err := retention.ApplyJobRequestKeyTx(ctx, tx, protectedLedger(), project, id, scan.Inventory.Revision, *next, input, proof); err != nil {
		return "", err
	}
	return "DETACHING", tx.Commit()
}
