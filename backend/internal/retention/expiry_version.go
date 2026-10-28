package retention

import (
	"math"
	"time"
)

func expiredReferenceVersion(prior ReferenceVersion, deadline time.Time, proof ExpiredProjection) (ReferenceVersion, error) {
	if !validReferenceVersion(prior) || prior.Schema != 1 || prior.Reference.State != Retained || prior.Reference.Revision == math.MaxInt64 || prior.Reference.AccountingOpen || proof.ReferenceID != prior.Reference.ID || proof.OriginalPayloadHash != prior.Reference.PayloadHash || ValidateProjectionForRule(proof, prior.Reference.Rule) != nil {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	next := prior
	next.Schema = 2
	next.Expiration = &ReferenceExpiration{PriorRevision: prior.Reference.Revision, PriorHash: stableHash(prior), PriorRecordedAt: prior.RecordedAt, EvidenceHash: stableHash(proof), BaseDeadline: prior.Deadline}
	next.Reference.State, next.Reference.ExpiredAt = Expired, proof.ExpiredAt
	next.Reference.Revision++
	next.Deadline, next.RecordedAt = deadline, proof.ExpiredAt
	if !validExpiredReferenceVersion(next) {
		return ReferenceVersion{}, ErrInvalidInventory
	}
	return next, nil
}

// Schema 2 is an expiry transition only. The original retained version can be
// reconstructed exactly and must match its recorded digest. The SQL executor
// additionally checks that parent and the independently stored evidence exist,
// and resolves the effective deadline against the actual pinned source graph.
func validExpiredReferenceVersion(v ReferenceVersion) bool {
	e, r := v.Expiration, v.Reference
	// DATETIME(6) must round-trip the exact signed/hashed proof clock.
	if r.ExpiredAt.Nanosecond()%1000 != 0 || v.Deadline.Nanosecond()%1000 != 0 {
		return false
	}
	// A derived Content copy has no independent deadline while its project is
	// live. Its pinned source can still expire; zero means unbounded, not an
	// earlier deadline. Roots without sources must retain their exact deadline.
	if e == nil || v.Schema != 2 || r.State != Expired || r.Revision < 2 || e.PriorRevision != r.Revision-1 || !hash64.MatchString(e.PriorHash) || !hash64.MatchString(e.EvidenceHash) || r.AccountingOpen || r.ExpiredAt.IsZero() || !v.RecordedAt.Equal(r.ExpiredAt) || v.Deadline.IsZero() || (!e.BaseDeadline.IsZero() && v.Deadline.After(e.BaseDeadline)) || r.ExpiredAt.Before(v.Deadline) || r.ExpiredAt.Before(e.PriorRecordedAt) || r.LeaseUntil.After(r.ExpiredAt) {
		return false
	}
	if len(r.Sources) == 0 && !v.Deadline.Equal(e.BaseDeadline) {
		return false
	}
	prior := v
	prior.Schema, prior.Expiration = 1, nil
	prior.Reference.State, prior.Reference.ExpiredAt = Retained, time.Time{}
	prior.Reference.Revision = e.PriorRevision
	prior.Deadline, prior.RecordedAt = e.BaseDeadline, e.PriorRecordedAt
	return validReferenceVersion(prior) && stableHash(prior) == e.PriorHash
}
