package retention

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
)

type ObservedMaterial struct {
	MaterialReference
	TimeBasis            TimeBasis
	FirstVerifiedAt      time.Time
	ValueFirstVerifiedAt time.Time
}

// ObservedMaterialReferences connects an actual source snapshot to previously
// persisted observations. ValueFirstVerifiedAt is version evidence, not a new
// retention birth: editing a source cannot replace its FirstVerifiedAt clock.
// The caller must hold the source/project transaction and permission checks.
func ObservedMaterialReferences(ctx context.Context, tx *sql.Tx, project string, index *SourceIndex, now time.Time) ([]ObservedMaterial, error) {
	if tx == nil || now.IsZero() {
		return nil, ErrSourceOwnership
	}
	refs, err := index.MaterialReferences(project)
	if err != nil {
		return nil, err
	}
	return observeMaterialReferences(ctx, tx, project, refs, now)
}

func observeMaterialReferences(ctx context.Context, tx *sql.Tx, project string, refs []MaterialReference, now time.Time) ([]ObservedMaterial, error) {
	bySource := map[string]MaterialReference{}
	for _, ref := range refs {
		if previous, exists := bySource[ref.SourceID]; exists && (previous.SourcePayloadHash != ref.SourcePayloadHash || previous.Table != ref.Table || previous.OwnerKey != ref.OwnerKey) {
			return nil, ErrSourceOwnership
		}
		bySource[ref.SourceID] = ref
	}
	ids := make([]string, 0, len(bySource))
	for id := range bySource {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	type age struct{ first, value time.Time }
	ages := make(map[string]age, len(ids))
	for offset := 0; offset < len(ids); offset += 500 {
		batch := ids[offset:min(offset+500, len(ids))]
		var args []any
		for _, id := range batch {
			args = append(args, id, bySource[id].SourcePayloadHash)
		}
		tuples := strings.TrimSuffix(strings.Repeat("(?,?,FALSE),", len(batch)), ",")
		rows, err := tx.QueryContext(ctx, `SELECT s.id,s.project_id,s.owner_table,s.owner_key,s.field_key,s.time_basis,CAST(s.first_verified_at AS CHAR),v.payload_hash,CAST(v.first_verified_at AS CHAR)
FROM retention_source_slots s JOIN retention_source_values v ON v.source_id=s.id
WHERE (s.id,v.payload_hash,v.is_null) IN (`+tuples+`) FOR SHARE`, args...)
		if err != nil {
			return nil, err
		}
		count := 0
		for rows.Next() {
			var id, storedProject, table, owner, field, basis, firstText, hash, valueText string
			if err := rows.Scan(&id, &storedProject, &table, &owner, &field, &basis, &firstText, &hash, &valueText); err != nil {
				rows.Close()
				return nil, err
			}
			ref, exists := bySource[id]
			_, duplicate := ages[id]
			first, firstErr := time.ParseInLocation("2006-01-02 15:04:05.999999", firstText, time.UTC)
			value, valueErr := time.ParseInLocation("2006-01-02 15:04:05.999999", valueText, time.UTC)
			if !exists || duplicate || storedProject != project || table != ref.Table || owner != ref.OwnerKey || field != strings.SplitN(ref.Field, ".", 2)[0] || hash != ref.SourcePayloadHash || basis != string(LegacyRegistered) || firstErr != nil || valueErr != nil || first.IsZero() || value.IsZero() || first.After(value) || value.After(now) {
				rows.Close()
				return nil, ErrSourceOwnership
			}
			ages[id] = age{first, value}
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil || count != len(batch) {
			return nil, ErrSourceOwnership
		}
	}
	result := make([]ObservedMaterial, 0, len(refs))
	for _, ref := range refs {
		clock, exists := ages[ref.SourceID]
		if !exists {
			return nil, ErrSourceOwnership
		}
		result = append(result, ObservedMaterial{MaterialReference: ref, TimeBasis: LegacyRegistered, FirstVerifiedAt: clock.first, ValueFirstVerifiedAt: clock.value})
	}
	return result, nil
}
