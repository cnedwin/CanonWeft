package retention

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

type recoverySourceValue struct {
	Hash  string
	Null  bool
	First time.Time
}

type recoverySourceHistory struct {
	First  time.Time
	Values []recoverySourceValue
}

// Keep observation clocks independently of the cleared business payload. A
// regenerated registration date would extend retention and invalidate backups.
func readRecoverySourceHistoryTx(ctx context.Context, tx *sql.Tx, versions []ReferenceVersion) (map[string]recoverySourceHistory, error) {
	history := map[string]recoverySourceHistory{}
	count := 0
	for _, v := range versions {
		slot, exists := history[v.SourceID]
		if !exists {
			var project, table, owner, field, basis string
			if err := tx.QueryRowContext(ctx, `SELECT project_id,owner_table,owner_key,field_key,time_basis,first_verified_at FROM retention_source_slots WHERE id=? FOR SHARE`, v.SourceID).Scan(&project, &table, &owner, &field, &basis, &slot.First); err != nil {
				return nil, err
			}
			if project != v.ProjectID || table != v.Table || owner != v.OwnerKey || field != strings.SplitN(v.Field, ".", 2)[0] || basis != string(LegacyRegistered) {
				return nil, ErrInvalidInventory
			}
			rows, err := tx.QueryContext(ctx, `SELECT payload_hash,is_null,first_verified_at FROM retention_source_values WHERE source_id=? ORDER BY payload_hash,is_null LIMIT 250001`, v.SourceID)
			if err != nil {
				return nil, err
			}
			firstSeen := false
			for rows.Next() {
				var value recoverySourceValue
				if err = rows.Scan(&value.Hash, &value.Null, &value.First); err != nil {
					rows.Close()
					return nil, err
				}
				count++
				if count > 250000 || !hash64.MatchString(value.Hash) || value.First.Before(slot.First) || (value.Null && value.Hash != financialDigest(nil)) {
					rows.Close()
					return nil, ErrInvalidInventory
				}
				firstSeen = firstSeen || value.First.Equal(slot.First)
				slot.Values = append(slot.Values, value)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if !firstSeen {
				return nil, ErrInvalidInventory
			}
			history[v.SourceID] = slot
		}
		if !slot.First.Equal(v.Reference.FirstVerifiedAt) {
			return nil, ErrInvalidInventory
		}
		matched := false
		for _, value := range slot.Values {
			matched = matched || (!value.Null && value.Hash == v.SourcePayloadHash && value.First.Equal(v.ValueFirstVerifiedAt))
		}
		if !matched {
			return nil, ErrInvalidInventory
		}
	}
	return history, nil
}

func (p *RecoveryExpiryPlan) writeSourcesTx(ctx context.Context, tx *sql.Tx, mapping RecoveryExpiryMap) error {
	written := map[string]bool{}
	for _, v := range p.versions {
		slot, exists := p.sources[v.SourceID]
		if !exists {
			return ErrInvalidInventory
		}
		owner := recoveryExpiryOwner(v, mapping)
		field, err := recoveryExpiryField(v.Table, v.Field, mapping.Resources)
		if err != nil {
			return err
		}
		field = strings.SplitN(field, ".", 2)[0]
		id, err := SourceSlotID(mapping.TargetProject, v.Table, owner, field)
		if err != nil {
			return err
		}
		if written[id] {
			continue
		}
		written[id] = true
		var first time.Time
		err = tx.QueryRowContext(ctx, `SELECT first_verified_at FROM retention_source_slots WHERE id=?`, id).Scan(&first)
		if err == sql.ErrNoRows {
			_, err = tx.ExecContext(ctx, `INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,?,?,?)`, id, mapping.TargetProject, v.Table, owner, field, string(LegacyRegistered), lifecycleSQLTime(slot.First))
		} else if err == nil && !first.Equal(slot.First) {
			return ErrInvalidInventory
		}
		if err != nil {
			return err
		}
		for _, value := range slot.Values {
			err = tx.QueryRowContext(ctx, `SELECT first_verified_at FROM retention_source_values WHERE source_id=? AND payload_hash=? AND is_null=?`, id, value.Hash, value.Null).Scan(&first)
			if err == sql.ErrNoRows {
				_, err = tx.ExecContext(ctx, `INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,?,?)`, id, value.Hash, value.Null, lifecycleSQLTime(value.First))
			} else if err == nil && !first.Equal(value.First) {
				return ErrInvalidInventory
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}
