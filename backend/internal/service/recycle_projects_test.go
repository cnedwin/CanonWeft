package service

import (
	"testing"
	"time"

	"novelstudio/internal/authority"
)

func TestRecoveryWindowUsesVerifiedClockAndExclusiveDeadline(t *testing.T) {
	deleted := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name     string
		evidence authority.DeletionEvidence
		window   string
		fail     bool
	}{
		{"before deadline", authority.DeletionEvidence{Sequence: 1, DeletedAt: deleted, CheckedAt: deleted.Add(7*24*time.Hour - time.Microsecond), VerifiedUTC: true}, "WITHIN_WINDOW", false},
		{"at deadline", authority.DeletionEvidence{Sequence: 1, DeletedAt: deleted, CheckedAt: deleted.Add(7 * 24 * time.Hour), VerifiedUTC: true}, "EXPIRED", false},
		{"legacy", authority.DeletionEvidence{Sequence: 1, DeletedAt: deleted, CheckedAt: deleted}, "UNVERIFIED_CLOCK", false},
		{"missing proof", authority.DeletionEvidence{DeletedAt: deleted, CheckedAt: deleted, VerifiedUTC: true}, "", true},
		{"future clock", authority.DeletionEvidence{Sequence: 1, DeletedAt: deleted, CheckedAt: deleted.Add(-time.Second), VerifiedUTC: true}, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var item RecycleProject
			err := applyRecoveryWindow(&item, test.evidence)
			if (err != nil) != test.fail || (!test.fail && item.Window != test.window) {
				t.Fatalf("window=%s error=%v", item.Window, err)
			}
			if !test.evidence.VerifiedUTC && (item.DeletedAt != nil || item.RecoverUntil != nil) {
				t.Fatal("legacy wall clock presented as a verified instant")
			}
		})
	}
}
