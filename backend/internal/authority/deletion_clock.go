package authority

import (
	"context"
	"database/sql"
	"time"
)

// DeletionEvidence comes only from this source's protected ledger. A legacy
// wall-clock value is retained as evidence, never asserted to be an instant.
type DeletionEvidence struct {
	LedgerID    string
	Sequence    uint64
	DeletedAt   time.Time
	VerifiedUTC bool
	CheckedAt   time.Time
}

func (l Ledger) eventClockSupported(ctx context.Context, reader SnapshotReader) (bool, error) {
	if !identifier.MatchString(l.Schema) {
		return false, ErrUnavailable
	}
	var columnType, nullable, fallback string
	err := reader.QueryRowContext(ctx, "SELECT COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT FROM information_schema.columns WHERE table_schema=? AND table_name='events' AND column_name='clock_basis'", l.Schema).Scan(&columnType, &nullable, &fallback)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if columnType != "enum('LEGACY','UTC_V1')" || nullable != "NO" || fallback != "LEGACY" {
		return false, ErrUnavailable
	}
	return true, nil
}

func (l Ledger) DeletionEvidenceTx(ctx context.Context, tx *sql.Tx, project string) (DeletionEvidence, error) {
	var result DeletionEvidence
	if tx == nil || !id32.MatchString(project) {
		return result, ErrUnavailable
	}
	source, err := Identity(ctx, tx)
	if err != nil {
		return result, err
	}
	result.LedgerID, err = l.provenance(ctx, tx, source)
	if err != nil {
		return DeletionEvidence{}, err
	}
	table, err := l.table("events")
	if err != nil {
		return DeletionEvidence{}, err
	}
	hasClock, err := l.eventClockSupported(ctx, tx)
	if err != nil {
		return DeletionEvidence{}, err
	}
	clockColumn := "'LEGACY'"
	if hasClock {
		clockColumn = "clock_basis"
	}
	// A transaction may already own an old consistent snapshot. Locking reads
	// observe the current committed tombstone after acquiring the project lock.
	rows, err := tx.QueryContext(ctx, "SELECT sequence_id,kind,resource_id,actor_id,CAST(created_at AS CHAR),"+clockColumn+" FROM "+table+" WHERE ledger_id=? AND project_id=? AND kind IN ('project_created','project_deleted') ORDER BY sequence_id FOR SHARE", result.LedgerID, project)
	if err != nil {
		return DeletionEvidence{}, err
	}
	created := false
	for rows.Next() {
		var sequence uint64
		var kind, resource, actor, at, basis string
		if err := rows.Scan(&sequence, &kind, &resource, &actor, &at, &basis); err != nil {
			rows.Close()
			return DeletionEvidence{}, err
		}
		if sequence == 0 || resource != project || (actor != "" && !id32.MatchString(actor)) || (basis != "LEGACY" && basis != "UTC_V1") {
			rows.Close()
			return DeletionEvidence{}, ErrUnavailable
		}
		if kind == string(ProjectCreated) {
			if created || result.Sequence != 0 {
				rows.Close()
				return DeletionEvidence{}, ErrUnavailable
			}
			created = true
			continue
		}
		if !created || result.Sequence != 0 || (basis == "UTC_V1" && actor == "") {
			rows.Close()
			return DeletionEvidence{}, ErrUnavailable
		}
		result.DeletedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", at, time.UTC)
		if err != nil || result.DeletedAt.IsZero() {
			rows.Close()
			return DeletionEvidence{}, ErrUnavailable
		}
		result.Sequence, result.VerifiedUTC = sequence, basis == "UTC_V1"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return DeletionEvidence{}, err
	}
	if !created {
		return DeletionEvidence{}, ErrUnavailable
	}
	var now string
	if err := tx.QueryRowContext(ctx, "SELECT CAST(UTC_TIMESTAMP(6) AS CHAR)").Scan(&now); err != nil {
		return DeletionEvidence{}, err
	}
	result.CheckedAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", now, time.UTC)
	if err != nil || result.CheckedAt.IsZero() || (result.VerifiedUTC && result.DeletedAt.After(result.CheckedAt)) {
		return DeletionEvidence{}, ErrUnavailable
	}
	return result, nil
}
