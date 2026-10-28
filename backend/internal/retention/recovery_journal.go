package retention

import (
	"context"
	"database/sql"
	"time"
)

// Read complete prefixes, including dependencies of superseded versions. Only
// pinned revisions are requested; newer unrelated source activity is excluded.
func readRecoveryJournalTx(ctx context.Context, tx *sql.Tx, project string, current map[string]ReferenceVersion, now time.Time) (map[referenceVersionKey]ReferenceVersion, error) {
	known := map[referenceVersionKey]ReferenceVersion{}
	wanted, loaded := map[string]int64{}, map[string]int64{}
	for id, v := range current {
		wanted[id] = v.Reference.Revision
	}
	bytes, edges := 0, 0
	for len(wanted) > 0 {
		batch := wanted
		wanted = map[string]int64{}
		for id, through := range batch {
			if through <= loaded[id] {
				continue
			}
			if through > 65536 {
				return nil, ErrInvalidInventory
			}
			rows, err := tx.QueryContext(ctx, `SELECT snapshot,record_hash FROM retention_reference_versions WHERE project_id=? AND reference_id=? AND revision>? AND revision<=? ORDER BY revision`, project, id, loaded[id], through)
			if err != nil {
				return nil, err
			}
			revision := loaded[id]
			for rows.Next() {
				var raw, hash string
				if err = rows.Scan(&raw, &hash); err != nil {
					rows.Close()
					return nil, err
				}
				bytes += len(raw)
				v, e := decodeReferenceVersion(raw, hash)
				revision++
				if e != nil || bytes > 64<<20 || len(known) >= 65536 || v.ProjectID != project || v.Reference.ID != id || v.Reference.Revision != revision || v.RecordedAt.After(now) {
					rows.Close()
					return nil, ErrInvalidInventory
				}
				known[referenceKey(v.Reference)] = v
				for _, pin := range v.Reference.Sources {
					edges++
					if edges > 1000000 {
						rows.Close()
						return nil, ErrInvalidInventory
					}
					wanted[pin.ReferenceID] = max(wanted[pin.ReferenceID], pin.Revision)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
			if revision != through {
				return nil, ErrInvalidInventory
			}
			loaded[id] = through
		}
	}
	for id, v := range current {
		if stored, ok := known[referenceKey(v.Reference)]; !ok || stored.Reference.ID != id || stableHash(stored) != stableHash(v) {
			return nil, ErrInvalidInventory
		}
	}
	return known, nil
}
