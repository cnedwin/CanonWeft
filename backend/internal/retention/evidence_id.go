package retention

import "strconv"

// EvidenceID is the sole identity for one source reference revision. A retry
// with different projection bytes or expiry time must conflict, not obtain a
// new evidence ID. Callers also enforce UNIQUE(reference_id,reference_revision).
func EvidenceID(referenceID string, revision int64) (string, error) {
	if !hash64.MatchString(referenceID) || revision < 1 {
		return "", ErrInvalidInventory
	}
	return stableHash([]string{"retention-evidence-v1", referenceID, strconv.FormatInt(revision, 10)}), nil
}
