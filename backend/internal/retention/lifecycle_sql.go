package retention

import (
	"context"
	"database/sql"
	"math"
	"slices"
	"strconv"
	"time"

	"novelstudio/internal/authority"
)

type RegisteredLifecycle struct {
	ProjectID       string
	FirstVerifiedAt time.Time
	Revision        int64
	Lifecycle       Lifecycle
}

// Reserved for the complete retention migration. Pending SQL and similarly
// named experiments cannot activate production expiry registration.
const MigrationName = "0036_retention.sql"

// RegisterProjectLifecycle runs inside the caller's authorized transaction.
// It does not create tables, commit, restore access or authorize content removal.
// The project lock is shared with publishers and deletion; only the current
// protected ledger supplies the deletion identity and clock proof.
func RegisterProjectLifecycle(ctx context.Context, tx *sql.Tx, ledger authority.Ledger, project string) (RegisteredLifecycle, error) {
	var result RegisteredLifecycle
	if tx == nil || !id32.MatchString(project) {
		return result, ErrInvalidInventory
	}
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id=? FOR UPDATE", project).Scan(&locked); err != nil {
		return result, err
	}
	evidence, err := ledger.DeletionEvidenceTx(ctx, tx, project)
	if err != nil {
		return result, err
	}
	stored, ledgerID, found, err := readRegisteredLifecycle(ctx, tx, project)
	if err != nil {
		return result, err
	}
	if found && (ledgerID != evidence.LedgerID || stored.FirstVerifiedAt.After(evidence.CheckedAt)) {
		return result, ErrInvalidInventory
	}
	if found && stored.Lifecycle.DeleteSequence != 0 {
		lifecycle := stored.Lifecycle
		if lifecycle.DeleteSequence != evidence.Sequence || !lifecycle.SourceDeletedAt.Equal(evidence.DeletedAt) || (lifecycle.TimeBasis == SourceUTC) != evidence.VerifiedUTC || lifecycle.DeletedAt.After(evidence.CheckedAt) {
			return result, ErrInvalidInventory
		}
		return stored, nil
	}
	if found && evidence.Sequence == 0 {
		return stored, nil
	}
	result = RegisteredLifecycle{ProjectID: project, FirstVerifiedAt: evidence.CheckedAt, Revision: 1}
	if found {
		if stored.Revision == math.MaxInt64 {
			return RegisteredLifecycle{}, ErrInvalidInventory
		}
		result.FirstVerifiedAt, result.Revision = stored.FirstVerifiedAt, stored.Revision+1
	}
	if evidence.Sequence != 0 {
		if evidence.VerifiedUTC {
			result.Lifecycle, err = DeletedLifecycle(evidence.LedgerID, evidence.Sequence, evidence.DeletedAt)
		} else {
			result.Lifecycle, err = LegacyDeletedLifecycle(evidence.LedgerID, evidence.Sequence, evidence.DeletedAt, evidence.CheckedAt)
		}
		if err != nil {
			return RegisteredLifecycle{}, err
		}
	}
	l := result.Lifecycle
	basis := SourceUTC
	var sequence any
	if l.DeleteSequence != 0 {
		basis, sequence = l.TimeBasis, l.DeleteSequence
	}
	fields := []any{sequence, basis, lifecycleSQLTime(l.SourceDeletedAt), lifecycleSQLTime(l.DeletedAt), lifecycleSQLTime(l.RecoverUntil), lifecycleSQLTime(l.CleanupEligibleAt), lifecycleSQLTime(l.OnlineDeadline), lifecycleSQLTime(l.BackupDeadline)}
	if !found {
		args := []any{project, evidence.LedgerID, lifecycleSQLTime(result.FirstVerifiedAt)}
		args = append(args, fields...)
		_, err = tx.ExecContext(ctx, "INSERT INTO project_lifecycle(project_id,source_ledger_id,first_verified_at,delete_sequence,time_basis,source_deleted_at,deleted_at,recover_until,cleanup_eligible_at,online_deadline,backup_deadline,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,UTC_TIMESTAMP(6))", args...)
	} else {
		fields = append(fields, project, stored.Revision)
		var updated sql.Result
		updated, err = tx.ExecContext(ctx, "UPDATE project_lifecycle SET delete_sequence=?,time_basis=?,source_deleted_at=?,deleted_at=?,recover_until=?,cleanup_eligible_at=?,online_deadline=?,backup_deadline=?,inventory_revision=inventory_revision+1,scanned_at=NULL,state='UNSCANNED',updated_at=UTC_TIMESTAMP(6) WHERE project_id=? AND inventory_revision=?", fields...)
		if err == nil {
			var count int64
			count, err = updated.RowsAffected()
			if err == nil && count != 1 {
				err = ErrInvalidInventory
			}
		}
	}
	if err != nil {
		return RegisteredLifecycle{}, err
	}
	return result, nil
}

func lifecycleSQLTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	// DATETIME has no timezone. Passing time.Time lets the driver's configured
	// Loc shift it before encoding; explicit UTC text matches our CAST reads.
	return value.UTC().Format("2006-01-02 15:04:05.000000")
}

func readRegisteredLifecycle(ctx context.Context, tx *sql.Tx, project string) (RegisteredLifecycle, string, bool, error) {
	r := RegisteredLifecycle{ProjectID: project}
	var ledger, basis, first, state string
	var catalog int
	var sequence, source, deleted, recoverUntil, cleanup, online, backup sql.NullString
	err := tx.QueryRowContext(ctx, "SELECT source_ledger_id,time_basis,CAST(first_verified_at AS CHAR),inventory_revision,catalog_version,state,CAST(delete_sequence AS CHAR),CAST(source_deleted_at AS CHAR),CAST(deleted_at AS CHAR),CAST(recover_until AS CHAR),CAST(cleanup_eligible_at AS CHAR),CAST(online_deadline AS CHAR),CAST(backup_deadline AS CHAR) FROM project_lifecycle WHERE project_id=? FOR UPDATE", project).Scan(&ledger, &basis, &first, &r.Revision, &catalog, &state, &sequence, &source, &deleted, &recoverUntil, &cleanup, &online, &backup)
	if err == sql.ErrNoRows {
		return RegisteredLifecycle{}, "", false, nil
	}
	if err != nil {
		return RegisteredLifecycle{}, "", false, err
	}
	r.FirstVerifiedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", first, time.UTC)
	if err != nil || r.FirstVerifiedAt.IsZero() || !id32.MatchString(ledger) || r.Revision < 1 || (catalog < 1 || catalog > 3) || !slices.Contains([]string{"UNSCANNED", "CURRENT", "PURGING", "PURGED", "OVERDUE", "BLOCKED"}, state) {
		return RegisteredLifecycle{}, "", false, ErrInvalidInventory
	}
	if catalog >= 2 {
		var schema int
		if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version").Scan(&schema); err != nil || schema < 35+catalog || schema > 38 {
			return RegisteredLifecycle{}, "", false, ErrInvalidInventory
		}
	}
	if !sequence.Valid {
		if basis != string(SourceUTC) || source.Valid || deleted.Valid || recoverUntil.Valid || cleanup.Valid || online.Valid || backup.Valid || state == "PURGED" {
			return RegisteredLifecycle{}, "", false, ErrInvalidInventory
		}
		return r, ledger, true, nil
	}
	r.Lifecycle.LedgerID, r.Lifecycle.TimeBasis = ledger, TimeBasis(basis)
	r.Lifecycle.DeleteSequence, err = strconv.ParseUint(sequence.String, 10, 64)
	if err != nil || r.Lifecycle.DeleteSequence == 0 {
		return RegisteredLifecycle{}, "", false, ErrInvalidInventory
	}
	for _, entry := range []struct {
		stored sql.NullString
		target *time.Time
	}{
		{source, &r.Lifecycle.SourceDeletedAt}, {deleted, &r.Lifecycle.DeletedAt}, {recoverUntil, &r.Lifecycle.RecoverUntil},
		{cleanup, &r.Lifecycle.CleanupEligibleAt}, {online, &r.Lifecycle.OnlineDeadline}, {backup, &r.Lifecycle.BackupDeadline},
	} {
		if entry.stored.Valid {
			*entry.target, err = time.ParseInLocation("2006-01-02 15:04:05.999999", entry.stored.String, time.UTC)
			if err != nil {
				return RegisteredLifecycle{}, "", false, ErrInvalidInventory
			}
		}
	}
	if !validLifecycle(r.Lifecycle) || (r.Lifecycle.TimeBasis == LegacyRegistered && r.Lifecycle.DeletedAt.Before(r.FirstVerifiedAt)) {
		return RegisteredLifecycle{}, "", false, ErrInvalidInventory
	}
	return r, ledger, true, nil
}
