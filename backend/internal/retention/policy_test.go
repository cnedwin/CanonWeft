package retention

import (
	"testing"
	"time"
)

func TestPolicyUsesExactDurationsAndNeverExpiresLiveContent(t *testing.T) {
	born := time.Date(2024, 2, 29, 8, 0, 0, 123000, time.UTC)
	for _, tc := range []struct {
		class Class
		days  int
	}{{Candidate, 30}, {RawRun, 30}, {FailedMedia, 7}, {Debug, 7}, {Temporary, 1}, {Audit, 365}, {Backup, 35}} {
		t.Run(string(tc.class), func(t *testing.T) {
			got, err := Deadline(tc.class, born, Lifecycle{})
			if err != nil || !got.Equal(born.Add(time.Duration(tc.days)*24*time.Hour)) {
				t.Fatal("wrong fixed retention deadline", got, err)
			}
		})
	}
	for _, class := range []Class{Content, ProtectedAuthority} {
		got, err := Deadline(class, born, Lifecycle{})
		if err != nil || !got.IsZero() {
			t.Fatal("live content or protected tombstone expired", class, got, err)
		}
	}
}

func TestDeletionDatesRequireTrustedEvidenceAndKeepAccounting(t *testing.T) {
	deleted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	lifecycle, err := DeletedLifecycle("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 42, deleted)
	if err != nil || lifecycle.RecoverUntil != deleted.Add(7*24*time.Hour) || lifecycle.OnlineDeadline != deleted.Add(30*24*time.Hour) || lifecycle.BackupDeadline != deleted.Add(65*24*time.Hour) {
		t.Fatal("wrong deletion windows", lifecycle, err)
	}
	for _, tc := range []struct {
		ledger string
		seq    uint64
		at     time.Time
	}{{"", 42, deleted}, {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 0, deleted}, {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 42, time.Time{}}} {
		if _, err := DeletedLifecycle(tc.ledger, tc.seq, tc.at); err == nil {
			t.Fatal("unproven deletion accepted")
		}
	}
	got, err := Deadline(Content, deleted.Add(-24*time.Hour), lifecycle)
	if err != nil || got != lifecycle.RecoverUntil {
		t.Fatal("deleted content does not enter purge at recycle end", got, err)
	}
	got, err = Deadline(Audit, deleted, lifecycle)
	if err != nil || got != deleted.Add(365*24*time.Hour) {
		t.Fatal("project deletion erased expense metadata", got, err)
	}
	if _, err = Deadline(RawRun, time.Time{}, Lifecycle{}); err == nil {
		t.Fatal("unknown age authorized expiry")
	}
}

func TestLegacyDeletionCarriesExplicitGraceWithoutRewritingOriginalEventTime(t *testing.T) {
	legacy := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	seen := legacy.Add(100 * 24 * time.Hour)
	lifecycle, err := LegacyDeletedLifecycle("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 42, legacy, seen)
	if err != nil || lifecycle.TimeBasis != LegacyRegistered || lifecycle.SourceDeletedAt != legacy || lifecycle.DeletedAt != seen || lifecycle.OnlineDeadline != seen.Add(30*24*time.Hour) {
		t.Fatal("legacy deletion time was treated as trusted UTC", lifecycle, err)
	}
	if _, err := Deadline(Content, legacy, lifecycle); err != nil {
		t.Fatal("valid legacy deletion rejected", err)
	}
}

func TestLegacyCleanupGraceNeverReopensRecoveryWindow(t *testing.T) {
	old := time.Date(2025, 1, 1, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	lifecycle, err := LegacyDeletedLifecycle("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 42, old, now)
	if err != nil {
		t.Fatal(err)
	}
	if !lifecycle.RecoverUntil.IsZero() {
		t.Fatal("registering an old deletion reopened recovery", lifecycle.RecoverUntil)
	}
	deadline, err := Deadline(Content, old, lifecycle)
	if err != nil || deadline != now.Add(7*24*time.Hour) {
		t.Fatal("legacy cleanup grace was lost", deadline, err)
	}
}
