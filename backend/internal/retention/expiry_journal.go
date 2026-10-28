package retention

// ExpiryJournalRecord carries the immutable records needed to verify an expiry
// without querying the source database. It contains hashes and typed evidence,
// never the erased original body. Source ownership is checked separately.
type ExpiryJournalRecord struct {
	Version             string `json:"version"`
	VersionHash         string `json:"version_hash"`
	Parent              string `json:"parent"`
	ParentHash          string `json:"parent_hash"`
	EvidenceID          string `json:"evidence_id"`
	Evidence            string `json:"evidence"`
	EvidenceHash        string `json:"evidence_hash"`
	OriginalPayloadHash string `json:"original_payload_hash"`
	EvidenceExpiredAt   string `json:"evidence_expired_at"`
}

// ValidateExpiryJournalRecord verifies the canonical expiry transition, its
// actual parent and the independently stored proof. It grants no restore,
// execution, current-authority or deletion permission.
func ValidateExpiryJournalRecord(record ExpiryJournalRecord) (ReferenceVersion, ExpiredProjection, error) {
	if len(record.Version)+len(record.Parent)+len(record.Evidence) > 4<<20 {
		return ReferenceVersion{}, ExpiredProjection{}, ErrInvalidInventory
	}
	v, err := decodeReferenceVersion(record.Version, record.VersionHash)
	if err != nil {
		return ReferenceVersion{}, ExpiredProjection{}, err
	}
	proof, err := validateExpiryJournalVersion(v, record)
	if err != nil {
		return ReferenceVersion{}, ExpiredProjection{}, err
	}
	return v, proof, nil
}

func validateExpiryJournalVersion(v ReferenceVersion, record ExpiryJournalRecord) (ExpiredProjection, error) {
	if !validExpiredReferenceVersion(v) || (!supportedExpiryField(v.Table, v.Field) && !requestKeyRule(v.Table+"."+v.Field)) {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	parent, err := decodeReferenceVersion(record.Parent, record.ParentHash)
	id, idErr := EvidenceID(v.Reference.ID, v.Reference.Revision)
	proof, proofErr := ParseExpiredProjection([]byte(record.Evidence), v.Reference.Rule)
	if err != nil || idErr != nil || proofErr != nil || record.ParentHash != v.Expiration.PriorHash || parent.ProjectID != v.ProjectID || parent.Reference.ID != v.Reference.ID || parent.Reference.Revision != v.Expiration.PriorRevision || record.EvidenceID != id || record.EvidenceHash != v.Expiration.EvidenceHash || financialDigest([]byte(record.Evidence)) != record.EvidenceHash || record.OriginalPayloadHash != v.Reference.PayloadHash || proof.ReferenceID != v.Reference.ID || proof.OriginalPayloadHash != record.OriginalPayloadHash || !proof.ExpiredAt.Equal(v.Reference.ExpiredAt) || record.EvidenceExpiredAt != v.Reference.ExpiredAt.Format("2006-01-02 15:04:05.000000") {
		return ExpiredProjection{}, ErrInvalidInventory
	}
	return proof, nil
}
