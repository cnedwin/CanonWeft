package retention

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Called only while holding the project's coordination lock. This records
// first observations without renewing existing clocks or creating deletion
// receipts for missing files. Missing PRESENT objects require reconciliation.
func registerScannedObjects(ctx context.Context, tx *sql.Tx, project string, physical []PhysicalObject, now time.Time) ([]Object, error) {
	return registerScannedObjectsWithClaim(ctx, tx, project, physical, now, nil)
}

func registerScannedObjectsWithClaim(ctx context.Context, tx *sql.Tx, project string, physical []PhysicalObject, now time.Time, claim *ObjectDeleteClaim) ([]Object, error) {
	if len(physical) > 250000 {
		return nil, ErrInvalidInventory
	}
	present := make(map[string]bool, len(physical))
	for _, object := range physical {
		if !hash64.MatchString(object.Hash) || present[object.Hash] || object.Size < 0 || object.Size > 32<<20 {
			return nil, ErrInvalidInventory
		}
		present[object.Hash] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT object_hash,CAST(first_seen_at AS CHAR),CAST(lease_until AS CHAR),state,revision,CAST(deleted_at AS CHAR) FROM retention_objects WHERE project_id=? ORDER BY object_hash LIMIT 250001 FOR UPDATE", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := []Object{}
	known := map[string]bool{}
	for rows.Next() {
		var object Object
		var first string
		var lease, deleted sql.NullString
		if err := rows.Scan(&object.Hash, &first, &lease, &object.State, &object.Revision, &deleted); err != nil {
			return nil, err
		}
		object.FirstSeenAt, err = time.ParseInLocation("2006-01-02 15:04:05.999999", first, time.UTC)
		if err != nil || object.FirstSeenAt.IsZero() || object.FirstSeenAt.After(now) || object.Revision < 1 || !hash64.MatchString(object.Hash) || known[object.Hash] || len(objects) == 250000 {
			return nil, ErrInvalidInventory
		}
		if lease.Valid {
			object.LeaseUntil, err = time.ParseInLocation("2006-01-02 15:04:05.999999", lease.String, time.UTC)
			if err != nil || object.LeaseUntil.IsZero() {
				return nil, ErrInvalidInventory
			}
		}
		switch object.State {
		case "DELETING":
			if claim == nil || claim.Hash != object.Hash || claim.Revision != object.Revision || !claim.FirstSeenAt.Equal(object.FirstSeenAt) || !claim.LeaseUntil.Equal(object.LeaseUntil) || deleted.Valid {
				return nil, ErrInvalidInventory
			}
			// This projection is transaction-local. The durable object remains
			// DELETING until verified absence or safe claim release is committed.
			object.State = ObjectDeleted
			if present[object.Hash] {
				object.State = ObjectPresent
			}
		case ObjectPresent:
			if !present[object.Hash] || deleted.Valid {
				return nil, ErrInvalidInventory
			}
		case ObjectDeleted:
			at, err := time.ParseInLocation("2006-01-02 15:04:05.999999", deleted.String, time.UTC)
			if !deleted.Valid || err != nil || at.IsZero() || at.Before(object.FirstSeenAt) || at.After(now) || present[object.Hash] {
				return nil, ErrInvalidInventory
			}
		default:
			return nil, ErrInvalidInventory
		}
		known[object.Hash] = true
		objects = append(objects, object)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	var added []string
	for _, object := range physical {
		if known[object.Hash] {
			continue
		}
		if len(objects) == 250000 {
			return nil, ErrInvalidInventory
		}
		objects = append(objects, Object{Hash: object.Hash, FirstSeenAt: now, State: ObjectPresent, Revision: 1})
		added = append(added, object.Hash)
	}
	for offset := 0; offset < len(added); offset += 500 {
		batch := added[offset:min(offset+500, len(added))]
		var args []any
		for _, hash := range batch {
			args = append(args, project, hash, lifecycleSQLTime(now))
		}
		values := strings.TrimSuffix(strings.Repeat("(?,?,?,'PRESENT',1),", len(batch)), ",")
		if _, err := tx.ExecContext(ctx, "INSERT INTO retention_objects(project_id,object_hash,first_seen_at,state,revision) VALUES "+values, args...); err != nil {
			return nil, err
		}
	}
	return objects, nil
}
