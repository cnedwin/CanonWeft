package retention

import (
	"errors"
	"regexp"
	"time"
)

var id32 = regexp.MustCompile(`^[a-f0-9]{32}$`)
var hash64 = regexp.MustCompile(`^[a-f0-9]{64}$`)
var ErrInvalidInventory = errors.New("retention inventory is incomplete or invalid")

const day = 24 * time.Hour

// DeletedLifecycle accepts evidence already authenticated by the protected
// ledger adapter. This pure function validates shape, not database authority.
func DeletedLifecycle(ledger string, sequence uint64, at time.Time) (Lifecycle, error) {
	if !id32.MatchString(ledger) || sequence == 0 || at.IsZero() {
		return Lifecycle{}, ErrInvalidInventory
	}
	at = at.UTC()
	return Lifecycle{TimeBasis: SourceUTC, SourceDeletedAt: at, LedgerID: ledger, DeleteSequence: sequence, DeletedAt: at, RecoverUntil: at.Add(7 * day), CleanupEligibleAt: at.Add(7 * day), OnlineDeadline: at.Add(30 * day), BackupDeadline: at.Add(65 * day)}, nil
}

func LegacyDeletedLifecycle(ledger string, sequence uint64, sourceAt, registeredAt time.Time) (Lifecycle, error) {
	if sourceAt.IsZero() {
		return Lifecycle{}, ErrInvalidInventory
	}
	lifecycle, err := DeletedLifecycle(ledger, sequence, registeredAt)
	if err != nil {
		return Lifecycle{}, err
	}
	lifecycle.TimeBasis = LegacyRegistered
	lifecycle.SourceDeletedAt = sourceAt.UTC()
	// Registration grants a conservative cleanup delay, never renewed access.
	// The original protected event has no provably UTC deletion time.
	lifecycle.RecoverUntil = time.Time{}
	return lifecycle, nil
}

func validLifecycle(lifecycle Lifecycle) bool {
	if lifecycle == (Lifecycle{}) {
		return true
	}
	expected, err := DeletedLifecycle(lifecycle.LedgerID, lifecycle.DeleteSequence, lifecycle.DeletedAt)
	if lifecycle.TimeBasis == LegacyRegistered {
		expected, err = LegacyDeletedLifecycle(lifecycle.LedgerID, lifecycle.DeleteSequence, lifecycle.SourceDeletedAt, lifecycle.DeletedAt)
	}
	return err == nil && expected == lifecycle
}

func referenceBirth(ref Reference, now time.Time) (time.Time, error) {
	if ref.CreatedAt.IsZero() {
		return time.Time{}, ErrInvalidInventory
	}
	switch ref.TimeBasis {
	case SourceUTC:
		if ref.CreatedAt.After(now) {
			return time.Time{}, ErrInvalidInventory
		}
		return ref.CreatedAt.UTC(), nil
	case LegacyRegistered:
		if ref.FirstVerifiedAt.IsZero() || ref.FirstVerifiedAt.After(now) {
			return time.Time{}, ErrInvalidInventory
		}
		return ref.FirstVerifiedAt.UTC(), nil
	default:
		return time.Time{}, ErrInvalidInventory
	}
}

// A zero deadline means retained, not expired. All TTLs are elapsed UTC days;
// object mtimes, file copies, and replay times cannot extend original ages.
func Deadline(class Class, born time.Time, lifecycle Lifecycle) (time.Time, error) {
	if born.IsZero() || !validLifecycle(lifecycle) {
		return time.Time{}, ErrInvalidInventory
	}
	var deadline time.Time
	switch class {
	case Content, ProtectedAuthority:
	case Candidate, RawRun:
		deadline = born.Add(30 * day)
	case FailedMedia, Debug:
		deadline = born.Add(7 * day)
	case Temporary:
		deadline = born.Add(day)
	case Audit:
		deadline = born.Add(365 * day)
	case Backup:
		deadline = born.Add(35 * day)
	default:
		return time.Time{}, ErrInvalidInventory
	}
	if lifecycle.DeletedAt.IsZero() || class == Audit || class == ProtectedAuthority {
		return deadline.UTC(), nil
	}
	cap := lifecycle.CleanupEligibleAt
	if class == Backup {
		cap = lifecycle.BackupDeadline
	}
	if deadline.IsZero() || cap.Before(deadline) {
		deadline = cap
	}
	return deadline.UTC(), nil
}
