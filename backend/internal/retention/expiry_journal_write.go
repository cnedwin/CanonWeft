package retention

import (
	"context"
	"database/sql"
	"encoding/json"
)

// Shared atomic evidence/history/current-reference write, after the caller has
// checked the source and performed its representation-specific replacement.
func persistExpiryVersionTx(ctx context.Context, tx *sql.Tx, project string, next ReferenceVersion, proof ExpiredProjection) error {
	if tx == nil || next.ProjectID != project || !validExpiredReferenceVersion(next) || stableHash(proof) != next.Expiration.EvidenceHash {
		return ErrInvalidInventory
	}
	encoded, _ := json.Marshal(proof)
	record, _ := json.Marshal(next)
	id, err := EvidenceID(next.Reference.ID, next.Reference.Revision)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO retention_evidence(id,project_id,reference_id,reference_revision,original_payload_hash,projection_hash,projection,expired_at) VALUES(?,?,?,?,?,?,?,?)", id, project, next.Reference.ID, next.Reference.Revision, next.Reference.PayloadHash, financialDigest(encoded), encoded, lifecycleSQLTime(proof.ExpiredAt)); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO retention_reference_versions(reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,recorded_at) VALUES(?,?,?,?,?,?,?,?)", next.Reference.ID, next.Reference.Revision, project, next.SourceID, next.SourcePayloadHash, financialDigest(record), record, lifecycleSQLTime(next.RecordedAt)); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "UPDATE retention_references SET deadline=?,state='EXPIRED',expired_at=?,revision=?,updated_at=? WHERE id=? AND revision=? AND state='RETAINED'", lifecycleSQLTime(next.Deadline), lifecycleSQLTime(next.Reference.ExpiredAt), next.Reference.Revision, lifecycleSQLTime(next.RecordedAt), next.Reference.ID, next.Expiration.PriorRevision)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return ErrInvalidInventory
	}
	return nil
}
