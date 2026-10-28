package retention

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var ErrLiveExpiryConsumers = errors.New("source still has retained consumers")

func sameExpiryOwnerBatch(first, other ReferenceVersion) bool {
	if first.Table != other.Table || first.OwnerKey != other.OwnerKey || requestKeyRule(first.Reference.Rule) || requestKeyRule(other.Reference.Rule) {
		return first.Reference.ID == other.Reference.ID
	}
	// Step inputs and outputs are independent columns. Retire them in reverse
	// dependency order, not with the frozen contract that validates their origin.
	if first.Table == "recipe_steps" || first.Table == "artifact_releases" {
		return first.Reference.ID == other.Reference.ID
	}
	return true
}

// Only consumers that are actually in this atomic owner batch may be ignored.
// Shared owner identity alone is insufficient; a retained sibling may be live.
func ExpiryHasLiveConsumersTx(ctx context.Context, tx *sql.Tx, project string, first Expiry, planned []Expiry, at time.Time) (bool, error) {
	current, err := readReferenceVersions(ctx, tx, project, at)
	if err != nil {
		return false, err
	}
	target, ok := current[first.ReferenceID]
	if !ok {
		return false, ErrInvalidInventory
	}
	if target.Table == "outbox" {
		var key []string
		if json.Unmarshal([]byte(target.OwnerKey), &key) != nil || len(key) != 1 {
			return false, ErrInvalidInventory
		}
		var processed sql.NullString
		if err := tx.QueryRowContext(ctx, "SELECT CAST(processed_at AS CHAR) FROM outbox WHERE id=? AND project_id=?", key[0], project).Scan(&processed); err != nil {
			return false, err
		}
		if !processed.Valid {
			return true, nil
		}
	}
	batch := map[string]bool{}
	for _, item := range planned {
		v, ok := current[item.ReferenceID]
		if ok && v.Reference.State == Retained && v.Reference.Revision == item.Revision && v.Reference.PayloadHash == item.PayloadHash && sameExpiryOwnerBatch(target, v) {
			batch[item.ReferenceID] = true
		}
	}
	for _, v := range current {
		if v.Reference.State != Retained || batch[v.Reference.ID] {
			continue
		}
		for _, source := range v.Reference.Sources {
			if batch[source.ReferenceID] {
				return true, nil
			}
		}
	}
	if live, err := publicationConsumersTx(ctx, tx, target, current, batch); live || err != nil {
		return live, err
	}
	if live, err := skillConsumersTx(ctx, tx, target, current, batch); live || err != nil {
		return live, err
	}
	if live, err := labelConsumersTx(ctx, tx, target, current, batch); live || err != nil {
		return live, err
	}
	if target.Table == "knowledge_indexes" && target.Field == "manifest" {
		var owner []string
		if json.Unmarshal([]byte(target.OwnerKey), &owner) != nil || len(owner) != 1 {
			return false, ErrInvalidInventory
		}
		// Published chunks are Content roots without TTL source pins, but a
		// retained child still needs the exact immutable manifest for validation.
		var live bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM knowledge_chunks c JOIN retention_references r ON r.owner_table='knowledge_chunks' AND r.owner_key=CONCAT('["',c.id,'"]') WHERE c.index_id=? AND r.project_id=? AND r.state='RETAINED')`, owner[0], project).Scan(&live)
		if err != nil || live {
			return live, err
		}
	}
	if target.Table == "source_imports" && (target.Field == "report" || target.Field == "source_hash") {
		var owner []string
		if json.Unmarshal([]byte(target.OwnerKey), &owner) != nil || len(owner) != 1 {
			return false, ErrInvalidInventory
		}
		for _, v := range current {
			if v.Reference.State != Retained || batch[v.Reference.ID] {
				continue
			}
			// A saved draft is an author root, so it has no dependency-TTL
			// pins. Still preserve the report/original until that draft is gone.
			if v.Table == "import_drafts" && v.Field == "payload" {
				var key []string
				if json.Unmarshal([]byte(v.OwnerKey), &key) != nil || len(key) != 2 {
					return false, ErrInvalidInventory
				}
				if key[0] == owner[0] {
					return true, nil
				}
			}
			if target.Field == "source_hash" && v.Table == "source_imports" && v.OwnerKey == target.OwnerKey && v.Field == "report" {
				return true, nil
			}
		}
	}
	if target.Table == "memory_revisions" && target.Field == "document" {
		var parts []string
		if json.Unmarshal([]byte(target.OwnerKey), &parts) != nil || len(parts) != 1 {
			return false, ErrInvalidInventory
		}
		// Adopted roots intentionally have no expiry-source pins. Their exact
		// head/summary links still prevent erasing a revision ahead of them.
		for _, table := range []string{"memory_items", "memory_revisions"} {
			head := ""
			args := []any{project, parts[0]}
			if table == "memory_items" {
				head = "m.head_revision=? OR "
				args = append(args, parts[0])
			}
			var live bool
			err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" m JOIN retention_references r ON r.project_id=m.project_id AND r.owner_table='"+table+"' AND r.owner_key=CONCAT('[\"',m.id,'\"]') AND r.field_key='document' AND r.state='RETAINED' WHERE m.project_id=? AND ("+head+"JSON_CONTAINS(m.document,JSON_OBJECT('revision',?), '$.summary.inputs')=1))", args...).Scan(&live)
			if err != nil || live {
				return live, err
			}
		}
	}
	return false, nil
}
