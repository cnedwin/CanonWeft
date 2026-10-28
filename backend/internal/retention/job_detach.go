package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"novelstudio/internal/authority"
)

var ErrUnsupportedExpiryOwner = errors.New("cleanup source requires a typed owner handler")

type ExpiryOwnerBatch struct {
	Source SourceKey
	Index  *SourceIndex
	Items  []Expiry
}

// Only literal typed primary keys can be reconstructed from the stored owner
// tuple. Digested free-text keys need their dedicated identity-retirement path.
func expiryOwnerSource(v ReferenceVersion) (SourceKey, error) {
	var parts []string
	columns := PrimaryKey(v.Table)
	if json.Unmarshal([]byte(v.OwnerKey), &parts) != nil || len(parts) != len(columns) || len(parts) == 0 {
		return SourceKey{}, ErrInvalidInventory
	}
	key := SourceKey{Table: v.Table, Primary: map[string]string{}}
	for i, column := range columns {
		if column.FreeText {
			return SourceKey{}, ErrUnsupportedExpiryOwner
		}
		key.Primary[column.Name] = parts[i]
	}
	owner, err := RowOwnerKey(key.Table, key.Primary)
	if err != nil || owner != v.OwnerKey {
		return SourceKey{}, ErrInvalidInventory
	}
	return key, nil
}

func expiryOwnerSourceTx(ctx context.Context, tx *sql.Tx, v ReferenceVersion) (SourceKey, error) {
	if v.Table != "capability_heads" {
		return expiryOwnerSource(v)
	}
	var parts []string
	if json.Unmarshal([]byte(v.OwnerKey), &parts) != nil || len(parts) != 2 || parts[0] != v.ProjectID || !strings.HasPrefix(parts[1], "sha256:") {
		return SourceKey{}, ErrInvalidInventory
	}
	hash := strings.TrimPrefix(parts[1], "sha256:")
	if !hash64.MatchString(hash) {
		return SourceKey{}, ErrInvalidInventory
	}
	rows, err := tx.QueryContext(ctx, "SELECT capability_key FROM capability_heads WHERE project_id=? AND (SHA2(CONVERT(capability_key USING binary),256)=? OR capability_key=?) LIMIT 2 FOR UPDATE", v.ProjectID, hash, retiredLabelPrefix+hash)
	if err != nil {
		return SourceKey{}, err
	}
	defer rows.Close()
	var value string
	count := 0
	for rows.Next() {
		if err := rows.Scan(&value); err != nil {
			return SourceKey{}, err
		}
		count++
	}
	if rows.Err() != nil || count != 1 {
		return SourceKey{}, ErrInvalidInventory
	}
	key := SourceKey{Table: v.Table, Primary: map[string]string{"project_id": v.ProjectID, "capability_key": value}}
	owner, err := RowOwnerKey(key.Table, key.Primary)
	if err != nil || owner != v.OwnerKey {
		return SourceKey{}, ErrInvalidInventory
	}
	return key, nil
}

// LoadExpiryOwnerBatchTx groups the original plan's fields by actual owner.
// A whole JSON value and its nested references must be prepared from one row,
// not from a partially replaced value. The caller holds the project lock.
func LoadExpiryOwnerBatchTx(ctx context.Context, tx *sql.Tx, project string, first Expiry, planned []Expiry, at time.Time) (ExpiryOwnerBatch, error) {
	current, err := readReferenceVersions(ctx, tx, project, at, first.ReferenceID)
	v, found := current[first.ReferenceID]
	if err != nil || !found || v.Reference.State != Retained || v.Reference.Revision != first.Revision || v.Reference.PayloadHash != first.PayloadHash || v.Reference.Rule != first.Rule {
		return ExpiryOwnerBatch{}, ErrInvalidInventory
	}
	source, err := expiryOwnerSourceTx(ctx, tx, v)
	if err != nil {
		return ExpiryOwnerBatch{}, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM retention_references WHERE project_id=? AND owner_table=? AND owner_key=? LIMIT 129 FOR UPDATE", project, v.Table, v.OwnerKey)
	if err != nil {
		return ExpiryOwnerBatch{}, err
	}
	ids := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ExpiryOwnerBatch{}, err
		}
		ids[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(ids) > 128 || (v.Table != "artifact_releases" && len(ids) > 32) {
		return ExpiryOwnerBatch{}, ErrInvalidInventory
	}
	batch := ExpiryOwnerBatch{Source: source, Items: []Expiry{}}
	for _, item := range planned {
		if !ids[item.ReferenceID] {
			continue
		}
		if requestKeyRule(item.Rule) {
			continue
		}
		if (v.Table == "recipe_steps" || v.Table == "artifact_releases") && item.ReferenceID != first.ReferenceID {
			continue
		}
		if !supportedExpiryField(v.Table, trimOwnerField(v.Table, item.Rule)) {
			return ExpiryOwnerBatch{}, ErrUnsupportedExpiryOwner
		}
		batch.Items = append(batch.Items, item)
	}
	if len(batch.Items) == 0 {
		return ExpiryOwnerBatch{}, ErrInvalidInventory
	}
	reader := &captureSourceReader{ctx: ctx, tx: tx}
	batch.Index, _ = NewSourceIndex(nil)
	batch.Index.loadSource = reader.read
	owner, err := batch.Index.Owner(source.Table, source.Primary)
	if err != nil || owner.Shared || owner.ProjectID != project {
		return ExpiryOwnerBatch{}, ErrSourceOwnership
	}
	return batch, nil
}

func trimOwnerField(table, rule string) string {
	prefix := table + "."
	if len(rule) <= len(prefix) || rule[:len(prefix)] != prefix {
		return ""
	}
	return rule[len(prefix):]
}

// ApplyJobDetachTx commits no transaction itself. It binds the existing typed
// eraser to the immutable saved plan and writes both receipts atomically with
// the original source, independent proof and reference history transition.
func ApplyJobDetachTx(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project, job string, revision int64, batch ExpiryOwnerBatch, proofs []ExpiredProjection) error {
	if err := validateJobDetachTx(ctx, tx, project, job, batch.Items, proofs); err != nil {
		return err
	}
	if err := ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, revision, batch.Items, proofs); err != nil {
		return err
	}
	if batch.Source.Table == "capability_heads" {
		for _, proof := range proofs {
			if proof.Materialization == AnonymizedLabel {
				batch.Source.Primary["capability_key"] = retiredLabelPrefix + proof.OriginalPayloadHash
			}
		}
	}
	// Re-read actual SQL and the independent proof before acknowledging progress.
	if err := ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, revision, batch.Items, proofs); err != nil {
		return err
	}
	return recordJobDetachTx(ctx, tx, project, job, batch.Items, proofs)
}

func validateJobDetachTx(ctx context.Context, tx *sql.Tx, project, job string, items []Expiry, proofs []ExpiredProjection) error {
	if len(items) == 0 || len(items) != len(proofs) {
		return ErrInvalidInventory
	}
	var raw, state, created string
	var disabled bool
	if err := tx.QueryRowContext(ctx, "SELECT plan,state,restored_disabled,CAST(created_at AS CHAR) FROM retention_jobs WHERE id=? AND project_id=? FOR UPDATE", job, project).Scan(&raw, &state, &disabled, &created); err != nil {
		return err
	}
	plan, err := ValidatePlanRecord(raw)
	createdAt, clockErr := time.ParseInLocation("2006-01-02 15:04:05.999999", created, time.UTC)
	if err != nil || clockErr != nil || disabled || state != "DETACHING" || plan.ProjectID != project {
		return ErrInvalidInventory
	}
	want := map[string]Expiry{}
	for _, item := range plan.Expire {
		want[item.ReferenceID] = item
	}
	for i, item := range items {
		original, ok := want[item.ReferenceID]
		if !ok || stableHash(original) != stableHash(item) || proofs[i].ReferenceID != item.ReferenceID || proofs[i].ExpiredAt.Before(createdAt) {
			return ErrInvalidInventory
		}
		var count int
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM retention_receipts WHERE job_id=? AND item_key=?", job, item.ReferenceID).Scan(&count); err != nil || count != 0 {
			return ErrInvalidInventory
		}
	}
	return nil
}

func recordJobDetachTx(ctx context.Context, tx *sql.Tx, project, job string, items []Expiry, proofs []ExpiredProjection) error {
	for i, item := range items {
		for _, phase := range []string{"DETACHED", "VERIFIED"} {
			if _, err := tx.ExecContext(ctx, "INSERT INTO retention_receipts(job_id,item_key,phase,result,evidence_hash,recorded_at) VALUES(?,?,?,'SUCCEEDED',?,UTC_TIMESTAMP(6))", job, item.ReferenceID, phase, stableHash(proofs[i])); err != nil {
				return err
			}
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE retention_jobs SET cursor_key=?,reason_code='',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=? AND state='DETACHING'", items[len(items)-1].ReferenceID, job, project)
	return err
}

// JobItemDetachedTx verifies saved progress against the current source and its
// independently persisted expiry proof. A receipt without actual erasure is
// not a checkpoint, and a later changed source requires a new plan.
func JobItemDetachedTx(ctx context.Context, tx *sql.Tx, project, job string, item Expiry, at time.Time) (bool, error) {
	rows, err := tx.QueryContext(ctx, "SELECT r.phase,r.result,r.evidence_hash,CAST(r.recorded_at AS CHAR),CAST(j.created_at AS CHAR),CAST(j.updated_at AS CHAR) FROM retention_receipts r JOIN retention_jobs j ON j.id=r.job_id WHERE r.job_id=? AND r.item_key=? AND j.project_id=? FOR UPDATE", job, item.ReferenceID, project)
	if err != nil {
		return false, err
	}
	receipts := map[string]string{}
	clocks := map[string]time.Time{}
	var createdAt time.Time
	for rows.Next() {
		var phase, result, hash, recorded, created, updated string
		if err := rows.Scan(&phase, &result, &hash, &recorded, &created, &updated); err != nil {
			rows.Close()
			return false, err
		}
		if result != "SUCCEEDED" || (phase != "DETACHED" && phase != "VERIFIED") {
			rows.Close()
			return false, ErrInvalidInventory
		}
		clock, clockErr := time.ParseInLocation("2006-01-02 15:04:05.999999", recorded, time.UTC)
		createdAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", created, time.UTC)
		updatedAt, updatedErr := time.ParseInLocation("2006-01-02 15:04:05.999999", updated, time.UTC)
		if clockErr != nil || err != nil || updatedErr != nil || clock.Before(createdAt) || clock.After(updatedAt) || updatedAt.After(at) {
			rows.Close()
			return false, ErrInvalidInventory
		}
		receipts[phase] = hash
		clocks[phase] = clock
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	if len(receipts) == 0 {
		return false, nil
	}
	if len(receipts) != 2 || receipts["DETACHED"] != receipts["VERIFIED"] || clocks["VERIFIED"].Before(clocks["DETACHED"]) {
		return false, ErrInvalidInventory
	}
	versions, err := readReferenceVersions(ctx, tx, project, at, item.ReferenceID)
	v, ok := versions[item.ReferenceID]
	if err != nil || !ok || v.Reference.State != Expired || v.Expiration == nil || v.Expiration.PriorRevision != item.Revision || v.Reference.PayloadHash != item.PayloadHash || v.Reference.Rule != item.Rule || !v.Deadline.Equal(item.Deadline) || v.Expiration.EvidenceHash != receipts["DETACHED"] {
		return false, ErrInvalidInventory
	}
	proof, err := verifyExpiryVersionStorageMode(ctx, tx, v, true)
	if err != nil {
		return false, err
	}
	if proof.ExpiredAt.Before(createdAt) || proof.ExpiredAt.After(clocks["DETACHED"]) {
		return false, ErrInvalidInventory
	}
	if proof.Materialization == AnonymizedRequestKey {
		return verifyJobRequestKeyTx(ctx, tx, project, v, proof, at)
	}
	source, err := expiryOwnerSourceTx(ctx, tx, v)
	if err != nil {
		return false, err
	}
	reader := captureSourceReader{ctx: ctx, tx: tx}
	row, err := reader.read(source.Table, source.Primary)
	if err != nil {
		return false, err
	}
	if err := verifyExpiryReplay(ctx, tx, project, row, []Expiry{item}, map[string]ExpiredProjection{item.ReferenceID: proof}, versions, at); err != nil {
		return false, err
	}
	return true, nil
}
