package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"
	"strings"
	"time"
)

type referenceChanges struct{ added, updated, unresolved, pinned, unbound int }

// registerMaterialReferences runs under the same project/source transaction as
// observation registration. It writes no bodies and performs no expiration.
func registerMaterialReferences(ctx context.Context, tx *sql.Tx, project string, index *SourceIndex, lifecycle RegisteredLifecycle, materials []ObservedMaterial, now time.Time, selected ...string) (referenceChanges, error) {
	var counts referenceChanges
	current, err := readReferenceVersions(ctx, tx, project, now, selected...)
	if err != nil {
		return counts, err
	}
	if len(selected) > 0 {
		// For unchanged content, a partial capture cannot revoke preservation
		// established by an adopted descendant outside its scope. Changed values
		// require fresh root evidence; historical versions keep their own roots.
		for i := range materials {
			if prior, ok := current[materials[i].ID]; ok && prior.Reference.ContentRoot && prior.Reference.PayloadHash == materials[i].PayloadHash && prior.SourcePayloadHash == materials[i].SourcePayloadHash {
				materials[i].ContentRoot = true
				// A changed mutable value is a new candidate, not the previously
				// retained content. Only identical source bytes inherit this root.
				// The narrow collector may have classified this as a derived
				// runtime copy because its retaining draft is outside the query.
				// Keep the existing root without attaching shorter-lived parents.
				materials[i].Sources = nil
				materials[i].UnboundSources = 0
			}
		}
	}
	if err := resolveDraftSourceHistory(ctx, tx, project, materials, current, now); err != nil {
		return counts, err
	}
	prepared, unbound, err := prepareMaterialVersions(project, lifecycle.Lifecycle, materials, current, now)
	if err != nil {
		return counts, err
	}
	counts.unbound = unbound
	roots := make(map[string]ReferenceVersion, len(current)+len(prepared))
	for id, v := range current {
		roots[id] = v
	}
	for _, v := range prepared {
		roots[v.Reference.ID] = v
	}
	history, err := readPinnedReferenceHistory(ctx, tx, project, roots, lifecycle.Lifecycle, now)
	if err != nil {
		return referenceChanges{}, err
	}
	missingProofs, err := index.verifyPrivateCopies(ctx, project, roots, history)
	if err != nil {
		return referenceChanges{}, err
	}
	counts.unbound += missingProofs
	var newRows, versions []ReferenceVersion
	seen := make(map[string]bool, len(materials))
	for _, next := range prepared {
		seen[next.Reference.ID] = true
		if len(next.Reference.Sources) > 0 {
			counts.pinned++
		}
		prior, exists := current[next.Reference.ID]
		if !exists {
			newRows = append(newRows, next)
			versions = append(versions, next)
			counts.added++
			continue
		}
		if next.Reference.Revision == prior.Reference.Revision {
			if stableHash(next) != stableHash(prior) {
				return referenceChanges{}, ErrInvalidInventory
			}
			continue
		}
		args := referenceRowArgs(next)[7:]
		args = append(args, next.Reference.ID, project, prior.Reference.Revision)
		result, err := tx.ExecContext(ctx, `UPDATE retention_references SET object_hash=?,payload_hash=?,source_created_at=?,time_basis=?,first_verified_at=?,deadline=?,state=?,expired_at=?,content_root=?,accounting_open=?,lease_until=?,revision=?,updated_at=? WHERE id=? AND project_id=? AND revision=?`, args...)
		if err != nil {
			return referenceChanges{}, err
		}
		count, err := result.RowsAffected()
		if err != nil || count != 1 {
			return referenceChanges{}, ErrInvalidInventory
		}
		versions = append(versions, next)
		counts.updated++
	}
	for id := range current {
		if !seen[id] {
			if verified, ok := index.expired[id]; ok && stableHash(verified) == stableHash(current[id]) {
				continue
			}
			// A disappeared/empty source is not an expiration receipt. Keep the
			// evidence and block execution until its detach path is reconciled.
			counts.unresolved++
		}
	}
	for offset := 0; offset < len(newRows); offset += 500 {
		batch := newRows[offset:min(offset+500, len(newRows))]
		var args []any
		for _, v := range batch {
			args = append(args, referenceRowArgs(v)...)
		}
		rows := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?),", len(batch)), ",")
		_, err := tx.ExecContext(ctx, `INSERT INTO retention_references(id,project_id,owner_table,owner_key,field_key,class,storage_kind,object_hash,payload_hash,source_created_at,time_basis,first_verified_at,deadline,state,expired_at,content_root,accounting_open,lease_until,revision,updated_at) VALUES `+rows, args...)
		if err != nil {
			return referenceChanges{}, err
		}
	}
	for offset := 0; offset < len(versions); offset += 500 {
		batch := versions[offset:min(offset+500, len(versions))]
		var args []any
		for _, v := range batch {
			raw, err := json.Marshal(v)
			if err != nil {
				return referenceChanges{}, err
			}
			args = append(args, v.Reference.ID, v.Reference.Revision, project, v.SourceID, v.SourcePayloadHash, financialDigest(raw), string(raw), lifecycleSQLTime(v.RecordedAt))
		}
		rows := strings.TrimSuffix(strings.Repeat("(?,?,?,?,?,?,?,?),", len(batch)), ",")
		if _, err := tx.ExecContext(ctx, `INSERT INTO retention_reference_versions(reference_id,revision,project_id,source_slot_id,source_payload_hash,record_hash,snapshot,recorded_at) VALUES `+rows, args...); err != nil {
			return referenceChanges{}, err
		}
	}
	if counts.added+counts.updated > 0 || counts.unresolved > 0 || counts.unbound > 0 {
		if lifecycle.Revision == math.MaxInt64 {
			return referenceChanges{}, ErrInvalidInventory
		}
		state := "UNSCANNED"
		if counts.unresolved > 0 || counts.unbound > 0 {
			state = "BLOCKED"
		}
		if !lifecycle.Lifecycle.OnlineDeadline.IsZero() && !now.Before(lifecycle.Lifecycle.OnlineDeadline) {
			state = "OVERDUE"
		}
		result, err := tx.ExecContext(ctx, `UPDATE project_lifecycle SET inventory_revision=inventory_revision+1,scanned_at=NULL,state=?,updated_at=? WHERE project_id=? AND inventory_revision=? AND (? OR state<>? OR scanned_at IS NOT NULL)`, state, lifecycleSQLTime(now), project, lifecycle.Revision, counts.added+counts.updated > 0, state)
		if err != nil {
			return referenceChanges{}, err
		}
		count, err := result.RowsAffected()
		if err != nil || (counts.added+counts.updated > 0 && count != 1) || count > 1 {
			return referenceChanges{}, ErrInvalidInventory
		}
	}
	return counts, nil
}

func referenceRowArgs(v ReferenceVersion) []any {
	r := v.Reference
	var object any
	if r.ObjectHash != "" {
		object = r.ObjectHash
	}
	return []any{r.ID, v.ProjectID, v.Table, v.OwnerKey, v.Field, v.Class, v.Storage, object, r.PayloadHash,
		lifecycleSQLTime(r.CreatedAt), r.TimeBasis, lifecycleSQLTime(r.FirstVerifiedAt), lifecycleSQLTime(v.Deadline), r.State,
		lifecycleSQLTime(r.ExpiredAt), r.ContentRoot, r.AccountingOpen, lifecycleSQLTime(r.LeaseUntil), r.Revision, lifecycleSQLTime(v.RecordedAt)}
}

func readReferenceVersions(ctx context.Context, tx *sql.Tx, project string, now time.Time, selected ...string) (map[string]ReferenceVersion, error) {
	return readReferenceVersionsMode(ctx, tx, project, now, true, selected...)
}

// Read-only previews must not mix an existing snapshot with current locking
// reads. Registration and mutation keep the original locking mode.
func readReferenceVersionsMode(ctx context.Context, tx *sql.Tx, project string, now time.Time, current bool, selected ...string) (map[string]ReferenceVersion, error) {
	filter := ""
	lock := ""
	if current {
		lock = " FOR UPDATE"
	}
	args := []any{project}
	if len(selected) > 4096 {
		return nil, ErrInvalidInventory
	}
	if len(selected) > 0 {
		for _, id := range selected {
			if !hash64.MatchString(id) {
				return nil, ErrInvalidInventory
			}
			args = append(args, id)
		}
		filter = " AND r.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(selected)), ",") + ")"
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.project_id,r.owner_table,r.owner_key,r.field_key,r.class,r.storage_kind,r.object_hash,r.payload_hash,CAST(r.source_created_at AS CHAR),r.time_basis,CAST(r.first_verified_at AS CHAR),CAST(r.deadline AS CHAR),r.state,CAST(r.expired_at AS CHAR),r.content_root,r.accounting_open,CAST(r.lease_until AS CHAR),r.revision,CAST(r.updated_at AS CHAR),v.project_id,v.source_slot_id,v.source_payload_hash,v.record_hash,v.snapshot,CAST(v.recorded_at AS CHAR)
FROM retention_references r LEFT JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision
WHERE r.project_id=?`+filter+` ORDER BY r.id LIMIT 250001`+lock, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]ReferenceVersion{}
	bytes := 0
	for rows.Next() {
		var stored ReferenceVersion
		r := &stored.Reference
		var object, created, first, deadline, expired, lease, updated sql.NullString
		var versionProject, sourceID, sourceHash, recordHash, record, recorded sql.NullString
		if err := rows.Scan(&r.ID, &stored.ProjectID, &stored.Table, &stored.OwnerKey, &stored.Field, &stored.Class, &stored.Storage, &object, &r.PayloadHash, &created, &r.TimeBasis, &first, &deadline, &r.State, &expired, &r.ContentRoot, &r.AccountingOpen, &lease, &r.Revision, &updated, &versionProject, &sourceID, &sourceHash, &recordHash, &record, &recorded); err != nil {
			return nil, err
		}
		bytes += len(record.String) + len(stored.OwnerKey) + 1024
		if len(result) == 250000 || bytes > 64<<20 || !versionProject.Valid || !sourceID.Valid || !sourceHash.Valid || !recordHash.Valid || !record.Valid || !recorded.Valid || !created.Valid || !first.Valid || !updated.Valid || (stored.Storage == Blob) != object.Valid {
			return nil, ErrInvalidInventory
		}
		r.Rule, r.ObjectHash = stored.Table+"."+stored.Field, object.String
		for _, clock := range []struct {
			value  sql.NullString
			target *time.Time
		}{
			{created, &r.CreatedAt}, {first, &r.FirstVerifiedAt}, {deadline, &stored.Deadline}, {expired, &r.ExpiredAt}, {lease, &r.LeaseUntil}, {updated, &stored.RecordedAt},
		} {
			if clock.value.Valid {
				*clock.target, err = time.ParseInLocation("2006-01-02 15:04:05.999999", clock.value.String, time.UTC)
				if err != nil || clock.target.IsZero() {
					return nil, ErrInvalidInventory
				}
			}
		}
		v, err := decodeReferenceVersion(record.String, recordHash.String)
		if err != nil || v.RecordedAt.After(now) || versionProject.String != project || v.ProjectID != project || v.SourceID != sourceID.String || v.SourcePayloadHash != sourceHash.String {
			return nil, ErrInvalidInventory
		}
		recordedAt, err := time.ParseInLocation("2006-01-02 15:04:05.999999", recorded.String, time.UTC)
		if err != nil || !recordedAt.Equal(v.RecordedAt) {
			return nil, ErrInvalidInventory
		}
		// Copy only fields that are intentionally stored in the immutable record,
		// then require the complete current-row projection to match it exactly.
		stored.Schema, stored.SourceID, stored.SourcePayloadHash = v.Schema, v.SourceID, v.SourcePayloadHash
		stored.ValueFirstVerifiedAt, stored.LineageState, stored.Lifecycle = v.ValueFirstVerifiedAt, v.LineageState, v.Lifecycle
		stored.Accounting = v.Accounting
		stored.DraftVersion = v.DraftVersion
		stored.WorkingProjection = v.WorkingProjection
		stored.TextProof = v.TextProof
		stored.Expiration = v.Expiration
		stored.Reference.Sources = v.Reference.Sources
		if stableHash(stored) != stableHash(v) {
			return nil, ErrInvalidInventory
		}
		if _, duplicate := result[r.ID]; duplicate {
			return nil, ErrInvalidInventory
		}
		result[r.ID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, v := range result {
		if _, err := verifyExpiryVersionStorageMode(ctx, tx, v, current); err != nil {
			return nil, err
		}
	}
	return result, nil
}
