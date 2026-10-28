package retention

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"novelstudio/internal/authority"
)

// ApplyRequestKeyExpiryTx couples unique-key retirement to its original expiry
// journal. The caller owns the authorized transaction and MUST roll it back on
// any error. Lock order is actor -> workspace policy -> project -> source.
// This is not a complete scanner, scheduler or an HTTP cleanup permission.
func ApplyRequestKeyExpiryTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string, input RequestKeyRetirement, inventoryRevision int64, expiry Expiry, proof ExpiredProjection) (RetiredRequestKey, error) {
	fail := func(err error) (RetiredRequestKey, error) { return RetiredRequestKey{}, err }
	if tx == nil || inventoryRevision < 1 {
		return fail(ErrInvalidInventory)
	}
	expected, err := ProjectRetiredRequestKey(project, expiry, input, proof.ExpiredAt)
	if err != nil || stableHash(expected) != stableHash(proof) {
		return fail(ErrInvalidInventory)
	}
	if err := verifyRegistrationCatalog(ctx, tx); err != nil {
		return fail(err)
	}
	var workspace string
	if err := tx.QueryRowContext(ctx, "SELECT workspace_id FROM users WHERE id=? FOR UPDATE", input.ActorID).Scan(&workspace); err != nil {
		return fail(err)
	}
	if workspace != input.WorkspaceID {
		return fail(ErrInvalidInventory)
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO retention_identity_policies(workspace_id) VALUES(?) ON DUPLICATE KEY UPDATE workspace_id=VALUES(workspace_id)", workspace); err != nil {
		return fail(err)
	}
	var closed bool
	if err := tx.QueryRowContext(ctx, "SELECT legacy_writes_closed FROM retention_identity_policies WHERE workspace_id=? FOR UPDATE", workspace).Scan(&closed); err != nil {
		return fail(err)
	}
	// RegisterProjectLifecycle takes the project lock and authenticates the
	// current protected lifecycle after actor/policy locks are already held.
	lifecycle, err := RegisterProjectLifecycle(ctx, tx, ledger, project)
	if err != nil {
		return fail(err)
	}
	var clock string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&clock); err != nil {
		return fail(err)
	}
	now, err := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
	if err != nil || proof.ExpiredAt.After(now) {
		return fail(ErrInvalidInventory)
	}
	current, err := readReferenceVersions(ctx, tx, project, now, expiry.ReferenceID)
	if err != nil || len(current) != 1 {
		return fail(ErrInvalidInventory)
	}
	v := current[expiry.ReferenceID]
	if v.ProjectID != project || v.Table != input.Table || v.OwnerKey != input.OriginalOwnerKey || v.Field != "operation_id" || v.Storage != Inline || v.Reference.PayloadHash != expiry.PayloadHash || v.Reference.Rule != expiry.Rule {
		return fail(ErrInvalidInventory)
	}
	index, _ := NewSourceIndex(nil)
	reader := captureSourceReader{ctx: ctx, tx: tx}
	index.loadSource = reader.read
	primary := map[string]string{"id": input.SourceID}
	if input.Table == "operations" {
		primary = map[string]string{"actor_id": input.ActorID, "operation_id": input.OriginalKey}
	}
	if v.Reference.State == Expired {
		if v.Expiration == nil || v.Expiration.PriorRevision != expiry.Revision || v.Expiration.EvidenceHash != stableHash(proof) || !v.Deadline.Equal(expiry.Deadline) {
			return fail(ErrInvalidInventory)
		}
		if input.Table == "operations" {
			primary["operation_id"] = "retired-v1:" + expiry.PayloadHash
		}
		if _, err := index.sourceRow(input.Table, primary); err != nil {
			return fail(err)
		}
		if err := index.bindExpiryVersions(ctx, tx, project, current, now, true); err != nil {
			return fail(err)
		}
		original, physical, err := requestIdentityOwnerKeys(input.Table, expiry.PayloadHash, *proof.Summary.RequestIdentity)
		if err != nil {
			return fail(err)
		}
		return RetiredRequestKey{OriginalOwnerKey: original, CurrentOwnerKey: physical, AnonymousKey: "retired-v1:" + expiry.PayloadHash}, nil
	}
	if lifecycle.Revision != inventoryRevision || v.Lifecycle != lifecycle.Lifecycle || v.Reference.Revision != expiry.Revision || v.Reference.State != Retained || v.Reference.LeaseUntil.After(proof.ExpiredAt) {
		return fail(ErrInvalidInventory)
	}
	var scanState string
	var scanned sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT state,CAST(scanned_at AS CHAR) FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&scanState, &scanned); err != nil {
		return fail(err)
	}
	scannedAt, scanErr := time.ParseInLocation("2006-01-02 15:04:05.999999", scanned.String, time.UTC)
	if scanState != "CURRENT" || !scanned.Valid || scanErr != nil || scannedAt.After(now) || v.RecordedAt.After(scannedAt) {
		return fail(ErrInvalidInventory)
	}
	row, err := index.sourceRow(input.Table, primary)
	if err != nil {
		return fail(err)
	}
	owner, err := index.Owner(input.Table, row.Values)
	if err != nil || owner.Shared || owner.ProjectID != project || owner.WorkspaceID != input.WorkspaceID {
		return fail(ErrInvalidInventory)
	}
	actual, err := requestIdentityFromRow(row, owner)
	if err != nil || actual != *proof.Summary.RequestIdentity || row.Nulls["operation_id"] || row.Values["operation_id"] != input.OriginalKey {
		return fail(ErrInvalidInventory)
	}
	active := func(row SourceRow) bool {
		return slices.Contains([]string{"QUEUED", "RUNNING", "CANCELLING", "RESERVED", "DISPATCHED"}, row.Values["state"])
	}
	if active(row) {
		return fail(ErrInvalidInventory)
	}
	if input.Table == "operations" {
		table := operationOwnerTable(input.Kind)
		keys := PrimaryKey(table)
		if len(keys) != 1 {
			return fail(ErrInvalidInventory)
		}
		result, err := index.sourceRow(table, map[string]string{keys[0].Name: input.SourceID})
		if err != nil {
			return fail(err)
		}
		if active(result) {
			return fail(ErrInvalidInventory)
		}
	}
	history, err := readPinnedReferenceHistory(ctx, tx, project, current, lifecycle.Lifecycle, now)
	if err != nil {
		return fail(err)
	}
	var ancestors []Reference
	for key, record := range history {
		if key.ID != v.Reference.ID || key.Revision != v.Reference.Revision {
			ancestors = append(ancestors, record.Reference)
		}
	}
	deadlines, err := referenceDeadlinesWithHistory([]Reference{v.Reference}, ancestors, lifecycle.Lifecycle, now)
	if err != nil || !deadlines[v.Reference.ID].Equal(expiry.Deadline) {
		return fail(ErrInvalidInventory)
	}
	next, err := expiredReferenceVersion(v, expiry.Deadline, proof)
	if err != nil {
		return fail(err)
	}
	out, err := RetireRequestKeyTx(ctx, tx, input)
	if err != nil {
		return fail(err)
	}
	if err := persistExpiryVersionTx(ctx, tx, project, next, proof); err != nil {
		return fail(err)
	}
	if err := invalidateCapturedInventory(ctx, tx, project); err != nil {
		return fail(err)
	}
	return out, nil
}
