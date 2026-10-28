package backup

import (
	"errors"
	"novelstudio/internal/retention"
)

// Archive and online readers bind the same source identity and evidence format.
// Schema activation and actual expiry execution remain separate requirements.
type expiredArchiveReference = retention.StoredReference
type expiredArchiveOwner = retention.StoredOwner

var errExpiredArchiveEvidence = errors.New("expired archive evidence does not match its source reference")

// ValidateExpiredSourceJournal checks archived history against the independently
// decoded current reference and actual owner row. Successful validation is not
// permission to restore: live authority and archive schema gates still apply.
func ValidateExpiredSourceJournal(ref retention.StoredReference, owner retention.StoredOwner, record retention.ExpiryJournalRecord) (retention.ExpiredProjection, error) {
	v, proof, err := retention.ValidateExpiryJournalRecord(record)
	if err != nil {
		return retention.ExpiredProjection{}, errExpiredArchiveEvidence
	}
	if v.Table == "generation_tasks" && v.Field == "frozen" && proof.Summary.CallID != owner.CallID {
		return retention.ExpiredProjection{}, errExpiredArchiveEvidence
	}
	if ref.ID != v.Reference.ID || ref.ProjectID != v.ProjectID || ref.Table != v.Table || ref.OwnerKey != v.OwnerKey || ref.Field != v.Field || ref.Storage != v.Storage || ref.State != v.Reference.State || ref.Revision != v.Reference.Revision || ref.EvidenceRevision != v.Reference.Revision || ref.PayloadHash != v.Reference.PayloadHash || ref.ObjectHash != v.Reference.ObjectHash || ref.ProjectionHash != v.Expiration.EvidenceHash || !ref.Deadline.Equal(v.Deadline) || !ref.ExpiredAt.Equal(v.Reference.ExpiredAt) || !ref.EvidenceExpiredAt.Equal(proof.ExpiredAt) {
		return retention.ExpiredProjection{}, errExpiredArchiveEvidence
	}
	return validateExpiredArchiveReference(ref, owner, []byte(record.Evidence))
}

func validateExpiredArchiveReference(ref expiredArchiveReference, owner expiredArchiveOwner, evidence []byte) (retention.ExpiredProjection, error) {
	projection, err := retention.ValidateStoredProjection(ref, owner, evidence)
	if err != nil {
		return retention.ExpiredProjection{}, errExpiredArchiveEvidence
	}
	return projection, nil
}
