package retention

import (
	"context"
	"database/sql"
	"slices"
	"strings"
	"time"
)

// Only exact pinned versions are read. This does not substitute a newer source,
// load unrelated project history, retain old body bytes or grant execution.
func readPinnedReferenceHistory(ctx context.Context, tx *sql.Tx, project string, roots map[string]ReferenceVersion, lifecycle Lifecycle, now time.Time) (map[referenceVersionKey]ReferenceVersion, error) {
	known := make(map[referenceVersionKey]ReferenceVersion, len(roots))
	current := make([]Reference, 0, len(roots))
	for _, v := range roots {
		known[referenceKey(v.Reference)] = v
		current = append(current, v.Reference)
	}
	frontier := make([]ReferenceVersion, 0, len(roots))
	for _, v := range roots {
		frontier = append(frontier, v)
	}
	var historical []Reference
	bytes, edges := 0, 0
	for len(frontier) > 0 {
		wanted := map[referenceVersionKey]bool{}
		for _, v := range frontier {
			edges += len(v.Reference.Sources)
			if edges > 1000000 {
				return nil, ErrInvalidInventory
			}
			for _, pin := range v.Reference.Sources {
				key := pinKey(pin)
				if _, exists := known[key]; !exists {
					wanted[key] = true
				}
				if len(known)+len(wanted) > 250000 {
					return nil, ErrInvalidInventory
				}
			}
		}
		keys := make([]referenceVersionKey, 0, len(wanted))
		for key := range wanted {
			keys = append(keys, key)
		}
		slices.SortFunc(keys, func(a, b referenceVersionKey) int {
			if a.ID != b.ID {
				return strings.Compare(a.ID, b.ID)
			}
			if a.Revision < b.Revision {
				return -1
			}
			if a.Revision > b.Revision {
				return 1
			}
			return 0
		})
		if len(known)+len(keys) > 250000 {
			return nil, ErrInvalidInventory
		}
		var loaded []ReferenceVersion
		for offset := 0; offset < len(keys); offset += 500 {
			batch := keys[offset:min(offset+500, len(keys))]
			args := []any{project}
			for _, key := range batch {
				args = append(args, key.ID, key.Revision)
			}
			tuples := strings.TrimSuffix(strings.Repeat("(?,?),", len(batch)), ",")
			rows, err := tx.QueryContext(ctx, `SELECT reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,CAST(recorded_at AS CHAR) FROM retention_reference_versions WHERE project_id=? AND (reference_id,revision) IN (`+tuples+`) ORDER BY reference_id,revision FOR SHARE`, args...)
			if err != nil {
				return nil, err
			}
			count := 0
			for rows.Next() {
				var id, storedProject, sourceID, sourceHash, recordHash, raw, clock string
				var revision int64
				if err := rows.Scan(&id, &revision, &storedProject, &sourceID, &sourceHash, &recordHash, &raw, &clock); err != nil {
					rows.Close()
					return nil, err
				}
				bytes += len(raw) + 256
				key := referenceVersionKey{id, revision}
				v, err := decodeReferenceVersion(raw, recordHash)
				at, clockErr := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
				if err != nil || clockErr != nil || bytes > 64<<20 || !wanted[key] || storedProject != project || v.ProjectID != project || referenceKey(v.Reference) != key || v.SourceID != sourceID || v.SourcePayloadHash != sourceHash || !v.RecordedAt.Equal(at) || at.After(now) {
					rows.Close()
					return nil, ErrInvalidInventory
				}
				if _, duplicate := known[key]; duplicate {
					rows.Close()
					return nil, ErrInvalidInventory
				}
				known[key] = v
				historical = append(historical, v.Reference)
				loaded = append(loaded, v)
				count++
			}
			err = rows.Err()
			rows.Close()
			if err != nil || count != len(batch) {
				return nil, ErrInvalidInventory
			}
		}
		for _, v := range frontier {
			for _, pin := range v.Reference.Sources {
				parent, ok := known[pinKey(pin)]
				if !ok || parent.Reference.PayloadHash != pin.PayloadHash || parent.RecordedAt.After(v.RecordedAt) {
					return nil, ErrInvalidInventory
				}
			}
		}
		for _, v := range loaded {
			if _, err := verifyExpiryVersionStorage(ctx, tx, v); err != nil {
				return nil, err
			}
		}
		frontier = loaded
	}
	// Includes cycle, prohibited accounting-source and exact payload checks.
	if _, err := referenceDeadlinesWithHistory(current, historical, lifecycle, now); err != nil {
		return nil, err
	}
	return known, nil
}
