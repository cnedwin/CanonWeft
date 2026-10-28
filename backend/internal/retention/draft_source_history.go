package retention

import (
	"context"
	"database/sql"
	"sort"
	"strings"
	"time"
)

type sourceRequestLocation struct{ material, source int }

// Resolve explicit business/working projections and their exact auxiliary
// values. Old records without projection evidence cannot certify new claims.
func resolveDraftSourceHistory(ctx context.Context, tx *sql.Tx, project string, materials []ObservedMaterial, current map[string]ReferenceVersion, now time.Time) error {
	next := map[string]ObservedMaterial{}
	for _, m := range materials {
		next[m.ID] = m
	}
	preserved := make([]map[string]bool, len(materials))
	for i := range materials {
		m := &materials[i]
		preserved[i] = map[string]bool{}
		if prior, ok := current[m.ID]; ok && !m.ContentRoot && prior.Reference.PayloadHash == m.PayloadHash && prior.SourcePayloadHash == m.SourcePayloadHash {
			for _, pin := range prior.Reference.Sources {
				preserved[i][pin.ReferenceID] = true
				found := false
				for _, source := range m.Sources {
					found = found || source.ReferenceID == pin.ReferenceID
				}
				// Original auxiliary references remain fixed even if the source
				// rows have since disappeared; history verification still applies.
				if !found {
					if len(m.Sources) >= 4096 {
						return ErrInvalidInventory
					}
					m.Sources = append(m.Sources, MaterialSource{ReferenceID: pin.ReferenceID, PayloadHash: pin.PayloadHash, Revision: pin.Revision})
				}
			}
		}
	}
	processed := make([]int, len(materials))
	apply := func(location sourceRequestLocation, payload string, revision int64, projection *WorkingProjectionEvidence, proofAt time.Time) error {
		m := &materials[location.material]
		m.Sources[location.source].PayloadHash, m.Sources[location.source].Revision = payload, revision
		m.Sources[location.source].ResolvedAt, m.Sources[location.source].Unresolved = proofAt, false
		if projection != nil {
			for _, dep := range projection.Dependencies {
				if err := appendSourceRequest(&m.MaterialReference, MaterialSource{ReferenceID: dep.ReferenceID, PayloadHash: dep.PayloadHash, NotAfter: proofAt}); err != nil {
					return err
				}
				for j := range m.Sources {
					source := &m.Sources[j]
					if source.ReferenceID == dep.ReferenceID && !source.NotAfter.IsZero() && source.ResolvedAt.After(source.NotAfter) && !preserved[location.material][source.ReferenceID] {
						source.Revision, source.ResolvedAt = 0, time.Time{}
						processed[location.material] = min(processed[location.material], j)
					}
				}
			}
		}
		return nil
	}
	count, bytes := 0, 0
	requests := 0
	for round := 0; round < 3; round++ {
		wanted := map[string][]sourceRequestLocation{}
		pending := false
		for i := range materials {
			for processed[i] < len(materials[i].Sources) {
				requests++
				if requests > 1000000 {
					return ErrInvalidInventory
				}
				pending = true
				j := processed[i]
				processed[i]++
				source := materials[i].Sources[j]
				if preserved[i][source.ReferenceID] {
					continue
				}
				location := sourceRequestLocation{i, j}
				if parent, ok := next[source.ReferenceID]; ok && (source.NotAfter.IsZero() || !now.After(source.NotAfter)) && sourceRequestMatches(source, parent.DraftVersion, parent.WorkingProjection, parent.PayloadHash) {
					if err := apply(location, parent.PayloadHash, 0, parent.WorkingProjection, now); err != nil {
						return err
					}
					continue
				}
				if parent, ok := current[source.ReferenceID]; ok && (source.NotAfter.IsZero() || !parent.RecordedAt.After(source.NotAfter)) && sourceRequestMatches(source, parent.DraftVersion, parent.WorkingProjection, parent.Reference.PayloadHash) {
					if err := apply(location, parent.Reference.PayloadHash, parent.Reference.Revision, parent.WorkingProjection, parent.RecordedAt); err != nil {
						return err
					}
					continue
				}
				wanted[source.ReferenceID] = append(wanted[source.ReferenceID], location)
			}
		}
		if !pending {
			return nil
		}
		ids := make([]string, 0, len(wanted))
		for id := range wanted {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		resolved := map[sourceRequestLocation]bool{}
		for offset := 0; offset < len(ids); offset += 500 {
			batch := ids[offset:min(offset+500, len(ids))]
			args := []any{project}
			for _, id := range batch {
				args = append(args, id)
			}
			args = append(args, 250001-count)
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
			rows, err := tx.QueryContext(ctx, `SELECT reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,CAST(recorded_at AS CHAR) FROM retention_reference_versions WHERE project_id=? AND reference_id IN (`+placeholders+`) ORDER BY reference_id,revision LIMIT ? FOR SHARE`, args...)
			if err != nil {
				return err
			}
			for rows.Next() {
				var id, storedProject, sourceID, sourceHash, recordHash, raw, clock string
				var revision int64
				if err := rows.Scan(&id, &revision, &storedProject, &sourceID, &sourceHash, &recordHash, &raw, &clock); err != nil {
					rows.Close()
					return err
				}
				count++
				bytes += len(raw) + 256
				v, err := decodeReferenceVersion(raw, recordHash)
				at, clockErr := time.ParseInLocation("2006-01-02 15:04:05.999999", clock, time.UTC)
				if err != nil || clockErr != nil || count > 250000 || bytes > 64<<20 || wanted[id] == nil || storedProject != project || v.ProjectID != project || v.Reference.ID != id || v.Reference.Revision != revision || v.SourceID != sourceID || v.SourcePayloadHash != sourceHash || !v.RecordedAt.Equal(at) || at.After(now) {
					rows.Close()
					return ErrInvalidInventory
				}
				for _, location := range wanted[id] {
					source := materials[location.material].Sources[location.source]
					if !resolved[location] && (source.NotAfter.IsZero() || !v.RecordedAt.After(source.NotAfter)) && sourceRequestMatches(source, v.DraftVersion, v.WorkingProjection, v.Reference.PayloadHash) {
						if err := apply(location, v.Reference.PayloadHash, v.Reference.Revision, v.WorkingProjection, v.RecordedAt); err != nil {
							rows.Close()
							return err
						}
						resolved[location] = true
					}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return err
			}
		}
		for _, locations := range wanted {
			for _, location := range locations {
				if !resolved[location] {
					materials[location.material].Sources[location.source].Unresolved = true
				}
			}
		}
	}
	for i, m := range materials {
		if processed[i] != len(m.Sources) {
			return ErrInvalidInventory
		}
	}
	return nil
}
