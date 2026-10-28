package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/retention"
)

type RecoveryIPIDs struct {
	SourceProject, TargetProject string
	Branches, Revisions          map[string]string
}

type RecoveryIPCounts struct{ Branches, Versions, Drafts int }

type recoveryTable struct{ name, columns, ownerJoin, ownerColumn string }
type recoveryRow []sql.NullString

var recoveryIPTables = []recoveryTable{
	{"branches", "id,project_id,name,parent_id,base_revision,head_revision,revision", "", "v.project_id"},
	{"ip_revisions", "id,branch_id,base_head,payload,content_hash,status,created_by,created_at", " JOIN branches b ON b.id=v.branch_id", "b.project_id"},
	{"adoptions", "candidate_id,actor_id,content_hash,adopted_at", " JOIN ip_revisions r ON r.id=v.candidate_id JOIN branches b ON b.id=r.branch_id", "b.project_id"},
	{"ip_merge_candidates", "candidate_id,parent_head,old_base", " JOIN ip_revisions r ON r.id=v.candidate_id JOIN branches b ON b.id=r.branch_id", "b.project_id"},
	{"ip_amendments", "candidate_id,reason,approved_by,approved_hash", " JOIN ip_revisions r ON r.id=v.candidate_id JOIN branches b ON b.id=r.branch_id", "b.project_id"},
	{"ip_promotions", "candidate_id,payload", " JOIN ip_revisions r ON r.id=v.candidate_id JOIN branches b ON b.id=r.branch_id", "b.project_id"},
	{"ip_branch_deltas", "candidate_id,project_id,branch_id,base_revision,parent_revision,delta_hash,payload,created_by,created_at", " JOIN ip_revisions r ON r.id=v.candidate_id JOIN branches b ON b.id=r.branch_id", "b.project_id"},
	{"ip_drafts", "branch_id,actor_id,revision,base_head,payload,updated_at", " JOIN branches b ON b.id=v.branch_id", "b.project_id"},
}

func (t recoveryTable) value(row recoveryRow, column string) string {
	for i, name := range strings.Split(t.columns, ",") {
		if name == column {
			return row[i].String
		}
	}
	return ""
}
func (t recoveryTable) set(row recoveryRow, column, value string) {
	for i, name := range strings.Split(t.columns, ",") {
		if name == column {
			row[i] = sql.NullString{String: value, Valid: true}
			return
		}
	}
}

// Only fixed descriptors above may reach this helper. Values and project IDs
// remain parameters, and every table shares one bounded total byte budget.
func readRecoveryTable(ctx context.Context, tx *sql.Tx, table recoveryTable, project string, total *int) ([]recoveryRow, error) {
	columns := strings.Split(table.columns, ",")
	selects := make([]string, len(columns))
	for i, column := range columns {
		selects[i] = "CAST(v." + column + " AS CHAR)"
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(selects, ",")+" FROM "+table.name+" v"+table.ownerJoin+" WHERE "+table.ownerColumn+"=? ORDER BY v."+columns[0]+" LIMIT 65537", project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []recoveryRow{}
	for rows.Next() {
		if len(result) == 65536 {
			return nil, domain.ErrNotReady
		}
		row := make(recoveryRow, len(columns))
		args := make([]any, len(columns))
		for i := range row {
			args[i] = &row[i]
		}
		if err = rows.Scan(args...); err != nil {
			return nil, err
		}
		for _, cell := range row {
			*total += len(cell.String)
		}
		if *total > 64<<20 {
			return nil, domain.ErrNotReady
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// CopyRecoveryIPTx retains the entire IP version graph, including adaptation
// bases, proposed changes, adoption history and unfinished private drafts.
// It emits no outbox events and copies no semantic-review execution grants.
// The outer recovery transaction must also copy the remaining project domains.
func (s *IP) CopyRecoveryIPTx(ctx context.Context, tx *sql.Tx, actorID string, ids RecoveryIPIDs) (counts RecoveryIPCounts, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if !validRecoveryIDs(ids.Branches) || !validRecoveryIDs(ids.Revisions) {
		return counts, domain.ErrValidation
	}
	if _, err = s.Workspace.lockRecoveryProjectsTx(ctx, tx, actorID, ids.SourceProject, ids.TargetProject); err != nil {
		return counts, err
	}
	var existing int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM branches WHERE project_id=?", ids.TargetProject).Scan(&existing); err != nil {
		return counts, err
	}
	if existing != 0 {
		return counts, domain.ErrConflict
	}
	data := map[string][]recoveryRow{}
	total := 0
	for _, table := range recoveryIPTables {
		if data[table.name], err = readRecoveryTable(ctx, tx, table, ids.SourceProject, &total); err != nil {
			return counts, err
		}
	}
	expiry, err := prepareRecoveryExpiryRows(ctx, tx, ids.SourceProject, recoveryIPTables, data)
	if err != nil {
		return counts, err
	}
	branches, versions := recoveryIPTables[0], recoveryIPTables[1]
	if len(ids.Branches) != len(data["branches"]) || len(ids.Revisions) != len(data["ip_revisions"]) {
		return counts, domain.ErrConflict
	}
	branchRows, versionRows := map[string]recoveryRow{}, map[string]recoveryRow{}
	for _, row := range data["branches"] {
		if ids.Branches[branches.value(row, "id")] == "" || branches.value(row, "project_id") != ids.SourceProject {
			return counts, domain.ErrNotReady
		}
		branchRows[branches.value(row, "id")] = row
	}
	for _, row := range data["ip_revisions"] {
		if ids.Revisions[versions.value(row, "id")] == "" || ids.Branches[versions.value(row, "branch_id")] == "" {
			return counts, domain.ErrNotReady
		}
		status := versions.value(row, "status")
		if status != "PREPARED" && status != "READY" && status != "PUBLISHED" {
			return counts, domain.ErrNotReady
		}
		// Preserve source identities while destination rows are rewritten below.
		versionRows[versions.value(row, "id")] = append(recoveryRow{}, row...)
	}
	mapID := func(value string, ids map[string]string) (string, error) {
		if value == "" {
			return "", nil
		}
		if ids[value] == "" {
			return "", domain.ErrNotReady
		}
		return ids[value], nil
	}
	for _, row := range data["branches"] {
		seen := map[string]bool{}
		for id := branches.value(row, "id"); id != ""; {
			parent, ok := branchRows[id]
			if !ok || seen[id] {
				return counts, domain.ErrNotReady
			}
			seen[id] = true
			id = branches.value(parent, "parent_id")
		}
		for _, key := range []string{"base_revision", "head_revision"} {
			if id := branches.value(row, key); id != "" {
				version, ok := versionRows[id]
				if !ok || versions.value(version, "status") != "PUBLISHED" || expiry.expired("ip_revisions", version, versions, "payload", "content_hash") {
					return counts, domain.ErrNotReady
				}
			}
		}
	}
	// Keep immutable snapshot objects byte-for-byte. Domain IDs within a story
	// (characters, rules, events, source notes) are not database row identities.
	objects := map[string][]byte{}
	for _, row := range data["ip_revisions"] {
		var snapshot, stored domain.Snapshot
		hash := versions.value(row, "content_hash")
		payloadExpired := expiry.expired("ip_revisions", row, versions, "payload")
		objectExpired := expiry.expired("ip_revisions", row, versions, "content_hash")
		if !payloadExpired && (decodeRecoveryDocument([]byte(versions.value(row, "payload")), &snapshot) != nil || domain.ValidateSnapshot(snapshot) != nil || valueHash(snapshot) != hash) {
			return counts, domain.ErrNotReady
		}
		if _, ok := objects[hash]; !ok && !objectExpired {
			body, e := s.Blobs.ReadObject(ctx, ids.SourceProject, hash)
			if e != nil || digest(string(body)) != hash || (!payloadExpired && (decodeRecoveryDocument(body, &stored) != nil || !reflect.DeepEqual(snapshot, stored))) {
				return counts, domain.ErrNotReady
			}
			objects[hash] = body
		}
		if base := versions.value(row, "base_head"); base != "" {
			previous, ok := versionRows[base]
			if !ok || versions.value(previous, "status") != "PUBLISHED" {
				return counts, domain.ErrNotReady
			}
		}
	}
	visited := map[string]bool{}
	for id := range versionRows {
		path := map[string]bool{}
		for current := id; current != "" && !visited[current]; {
			if path[current] {
				return counts, domain.ErrNotReady
			}
			path[current] = true
			current = versions.value(versionRows[current], "base_head")
		}
		for id := range path {
			visited[id] = true
		}
	}
	// Transform the complete graph before creating the first destination row.
	for _, table := range recoveryIPTables {
		for _, row := range data[table.name] {
			candidate := table.value(row, "candidate_id")
			if candidate != "" {
				if _, ok := versionRows[candidate]; !ok {
					return counts, domain.ErrNotReady
				}
			}
			if !expiry.expired(table.name, row, table, "payload") {
				if err = rewriteRecoveryIPPayload(table, row, ids, versionRows, versions); err != nil {
					return counts, err
				}
			}
			for _, column := range strings.Split(table.columns, ",") {
				var mapping map[string]string
				switch column {
				case "project_id":
					table.set(row, column, ids.TargetProject)
					continue
				case "branch_id", "parent_id":
					mapping = ids.Branches
				case "id":
					if table.name == "branches" {
						mapping = ids.Branches
					} else {
						mapping = ids.Revisions
					}
				case "base_revision", "head_revision", "base_head", "candidate_id", "parent_head", "old_base", "parent_revision":
					mapping = ids.Revisions
				}
				if mapping == nil {
					continue
				}
				value := table.value(row, column)
				// Draft form fields may intentionally be unfinished. Preserve
				// unresolved text; future submission still revalidates it.
				if table.name == "ip_drafts" && column == "base_head" && value != "" && mapping[value] == "" {
					continue
				}
				mapped, e := mapID(value, mapping)
				if e != nil {
					return counts, e
				}
				table.set(row, column, mapped)
			}
		}
	}
	expiryMapping, expiryGraph, err := expiry.rebind(ids.SourceProject, ids.TargetProject, recoveryIPTables, data, retention.RecoveryExpiryMap{Revisions: ids.Revisions})
	if err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "SAVEPOINT ip_recovery_copy"); err != nil {
		return counts, err
	}
	defer func() {
		if err != nil {
			cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer finish()
			if _, e := tx.ExecContext(cleanup, "ROLLBACK TO SAVEPOINT ip_recovery_copy"); e != nil {
				_ = tx.Rollback()
			}
			counts = RecoveryIPCounts{}
		}
	}()
	for hash, body := range objects {
		object, e := objectwriter.Write(ctx, tx, s.Blobs, ids.TargetProject, body)
		if e != nil {
			return counts, e
		}
		if object.Hash != hash {
			return counts, domain.ErrNotReady
		}
	}
	for _, table := range recoveryIPTables {
		for _, row := range data[table.name] {
			args := make([]any, len(row))
			for i, cell := range row {
				if cell.Valid {
					args[i] = cell.String
				}
			}
			placeholders := strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")
			if _, err = tx.ExecContext(ctx, "INSERT INTO "+table.name+"("+table.columns+") VALUES("+placeholders+")", args...); err != nil {
				return counts, err
			}
		}
	}
	if err = expiry.plan.WriteTx(ctx, tx, expiryMapping, expiryGraph); err != nil {
		return counts, err
	}
	for _, table := range recoveryIPTables {
		for _, row := range data[table.name] {
			primary := map[string]string{}
			switch table.name {
			case "branches", "ip_revisions":
				primary["id"] = table.value(row, "id")
			case "ip_drafts":
				primary["branch_id"], primary["actor_id"] = table.value(row, "branch_id"), table.value(row, "actor_id")
			default:
				primary["candidate_id"] = table.value(row, "candidate_id")
			}
			if err = s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: table.name, Primary: primary}); err != nil {
				return counts, err
			}
		}
	}
	if err = recoveryWindowTx(ctx, tx, ids.SourceProject); err != nil {
		return counts, err
	}
	if _, err = tx.ExecContext(ctx, "RELEASE SAVEPOINT ip_recovery_copy"); err != nil {
		return counts, err
	}
	return RecoveryIPCounts{Branches: len(data["branches"]), Versions: len(data["ip_revisions"]), Drafts: len(data["ip_drafts"])}, nil
}

func rewriteRecoveryIPPayload(table recoveryTable, row recoveryRow, ids RecoveryIPIDs, versions map[string]recoveryRow, spec recoveryTable) error {
	mapKnown := func(value string, mapping map[string]string) string {
		if replacement := mapping[value]; replacement != "" {
			return replacement
		}
		return value
	}
	remapPromotion := func(p *domain.PromotionInput, strict bool) error {
		if strict && (ids.Branches[p.SourceBranch] == "" || ids.Revisions[p.SourceHead] == "" || p.BaseHead != "" && ids.Revisions[p.BaseHead] == "") {
			return domain.ErrNotReady
		}
		p.SourceBranch, p.SourceHead, p.BaseHead = mapKnown(p.SourceBranch, ids.Branches), mapKnown(p.SourceHead, ids.Revisions), mapKnown(p.BaseHead, ids.Revisions)
		p.OperationID = "recovery:" + ids.TargetProject + ":promotion:" + table.value(row, strings.Split(table.columns, ",")[0])
		return nil
	}
	if table.name == "adoptions" || table.name == "ip_amendments" {
		version := versions[table.value(row, "candidate_id")]
		key := "content_hash"
		if table.name == "ip_amendments" {
			key = "approved_hash"
		}
		if hash := table.value(row, key); hash != "" && hash != spec.value(version, "content_hash") {
			return domain.ErrNotReady
		}
	}
	switch table.name {
	case "ip_promotions":
		var p domain.PromotionInput
		if decodeRecoveryDocument([]byte(table.value(row, "payload")), &p) != nil || remapPromotion(&p, true) != nil {
			return domain.ErrNotReady
		}
		raw, e := json.Marshal(p)
		if e != nil {
			return e
		}
		table.set(row, "payload", string(raw))
	case "ip_branch_deltas":
		var d domain.BranchDelta
		raw := []byte(table.value(row, "payload"))
		version := versions[table.value(row, "candidate_id")]
		if table.value(row, "project_id") != ids.SourceProject || table.value(row, "branch_id") != spec.value(version, "branch_id") || digest(string(raw)) != table.value(row, "delta_hash") || decodeRecoveryDocument(raw, &d) != nil || d.Schema != 1 {
			return domain.ErrNotReady
		}
		for i := range d.Operations {
			for j := range d.Operations[i].SourceRefs {
				pin := &d.Operations[i].SourceRefs[j]
				original, ok := versions[pin.Revision]
				if !ok || pin.Hash != spec.value(original, "content_hash") || ids.Revisions[pin.Revision] == "" {
					return domain.ErrNotReady
				}
				switch pin.Kind {
				case "manual":
					if pin.ID != table.value(row, "created_by") || pin.Revision != table.value(row, "candidate_id") {
						return domain.ErrNotReady
					}
				case "server_timestamp":
					// Timestamp provenance retains the originating actor or branch.
					if pin.ID != table.value(row, "created_by") {
						if ids.Branches[pin.ID] == "" {
							return domain.ErrNotReady
						}
						pin.ID = ids.Branches[pin.ID]
					}
				case "merge", "promotion", "previous":
					if ids.Branches[pin.ID] == "" {
						return domain.ErrNotReady
					}
					pin.ID = ids.Branches[pin.ID]
				default:
					return domain.ErrNotReady
				}
				pin.Revision = ids.Revisions[pin.Revision]
			}
		}
		raw, e := json.Marshal(d)
		if e != nil {
			return e
		}
		table.set(row, "payload", string(raw))
		table.set(row, "delta_hash", digest(string(raw)))
	case "ip_drafts":
		raw := []byte(table.value(row, "payload"))
		if rejectExpiredRuntime(raw, "ip_drafts.payload") != nil {
			return domain.ErrMaterialExpired
		}
		var envelope map[string]json.RawMessage
		if json.Unmarshal(raw, &envelope) != nil {
			return domain.ErrNotReady
		}
		if _, ok := envelope["draft_schema"]; ok {
			var stored domain.StoredIPDraft
			if decodeRecoveryDocument(raw, &stored) != nil || (stored.DraftSchema != 1 && stored.DraftSchema != 2) {
				return domain.ErrNotReady
			}
		} else {
			var snapshot domain.Snapshot
			if decodeRecoveryDocument(raw, &snapshot) != nil {
				return domain.ErrNotReady
			}
		}
		revision, e := strconv.ParseInt(table.value(row, "revision"), 10, 64)
		if e != nil {
			return domain.ErrNotReady
		}
		draft, e := domain.DecodeIPDraft(raw, revision, table.value(row, "base_head"))
		if e != nil {
			return e
		}
		if draft.BranchActions != nil {
			if p := draft.BranchActions.Promotion; p != nil {
				if e = remapPromotion(p, false); e != nil {
					return e
				}
			}
			if m := draft.BranchActions.Merge; m != nil {
				m.BaseHead, m.UpstreamHead = mapKnown(m.BaseHead, ids.Revisions), mapKnown(m.UpstreamHead, ids.Revisions)
				m.OperationID = "recovery:" + ids.TargetProject + ":merge:" + table.value(row, "branch_id")
			}
		}
		raw, e = json.Marshal(domain.StoredIPDraft{DraftSchema: 2, Snapshot: draft.Snapshot, Amendment: draft.Amendment, AmendmentReason: draft.AmendmentReason, ChangeReasons: draft.ChangeReasons, BranchActions: draft.BranchActions})
		if e != nil {
			return e
		}
		table.set(row, "payload", string(raw))
	}
	return nil
}
