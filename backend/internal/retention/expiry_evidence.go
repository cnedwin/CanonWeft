package retention

import (
	"context"
	"database/sql"
)

// A canonical expired version alone is not persisted expiry evidence. Its
// immutable retained parent and independently stored proof must both exist.
func verifyExpiryVersionStorage(ctx context.Context, tx *sql.Tx, v ReferenceVersion) (ExpiredProjection, error) {
	return verifyExpiryVersionStorageMode(ctx, tx, v, true)
}

func verifyExpiryVersionStorageMode(ctx context.Context, tx *sql.Tx, v ReferenceVersion, current bool) (ExpiredProjection, error) {
	if v.Schema == 1 && v.Expiration == nil {
		return ExpiredProjection{}, nil
	}
	if !validExpiredReferenceVersion(v) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	var record ExpiryJournalRecord
	lock := ""
	if current {
		lock = " FOR SHARE"
	}
	err := tx.QueryRowContext(ctx, `SELECT p.snapshot,p.record_hash,e.id,e.projection,e.projection_hash,e.original_payload_hash,CAST(e.expired_at AS CHAR)
FROM retention_reference_versions p JOIN retention_evidence e ON e.project_id=p.project_id AND e.reference_id=p.reference_id AND e.reference_revision=?
WHERE p.project_id=? AND p.reference_id=? AND p.revision=?`+lock, v.Reference.Revision, v.ProjectID, v.Reference.ID, v.Expiration.PriorRevision).Scan(&record.Parent, &record.ParentHash, &record.EvidenceID, &record.Evidence, &record.EvidenceHash, &record.OriginalPayloadHash, &record.EvidenceExpiredAt)
	if err != nil {
		return ExpiredProjection{}, err
	}
	return validateExpiryJournalVersion(v, record)
}
