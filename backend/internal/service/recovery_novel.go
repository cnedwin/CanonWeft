package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/novel"
	"novelstudio/internal/retention"
)

// The setup belongs to a project only after confirmation. Workspace-only drafts
// are deliberately excluded from project recovery, including other authors'.
var recoveryNovelTables = []recoveryTable{
	{"novel_setup_drafts", "id,workspace_id,actor_id,revision,step,payload,content_hash,confirmed_novel_id,created_at,updated_at", " JOIN novels n ON n.setup_id=v.id", "n.project_id"},
	{"novels", "id,project_id,branch_id,created_by,setup_id,settings_revision,stage,created_at", "", "v.project_id"},
	{"novel_settings_versions", "novel_id,revision,payload,content_hash,created_by,created_at", " JOIN novels n ON n.id=v.novel_id", "n.project_id"},
	{"novel_settings_drafts", "novel_id,actor_id,revision,base_revision,payload,content_hash,updated_at", " JOIN novels n ON n.id=v.novel_id", "n.project_id"},
}

// Called only within the owning recovery transaction, after its IP graph exists.
// A bad/missing settings version fails the whole copy; it never drops the book.
func (s *IP) copyRecoveryNovelsTx(ctx context.Context, tx *sql.Tx, actor domain.Actor, ids RecoveryIPIDs) error {
	var schema int
	if err := tx.QueryRowContext(ctx, "SELECT version FROM schema_version WHERE id=1").Scan(&schema); err != nil {
		return err
	}
	if schema < 37 {
		return nil
	}
	if schema != 37 && schema != 38 {
		return domain.ErrNotReady
	}
	data := map[string][]recoveryRow{}
	total := 0
	tables := recoveryNovelTables
	if schema == 37 {
		tables = tables[:3]
	}
	for _, table := range tables {
		rows, err := readRecoveryTable(ctx, tx, table, ids.SourceProject, &total)
		if err != nil {
			return err
		}
		data[table.name] = rows
	}
	drafts, books, versions := recoveryNovelTables[0], recoveryNovelTables[1], recoveryNovelTables[2]
	bookIDs, setupIDs := map[string]string{}, map[string]string{}
	bookRows, setupRows := map[string]recoveryRow{}, map[string]recoveryRow{}
	for _, row := range data[books.name] {
		id := books.value(row, "id")
		bookIDs[id], bookRows[id] = NewID(), row
	}
	for _, row := range data[drafts.name] {
		id := drafts.value(row, "id")
		setupIDs[id], setupRows[id] = NewID(), row
	}
	if len(bookIDs) != len(setupIDs) {
		return domain.ErrNotReady
	}
	heads := map[string]bool{}
	for _, row := range data[versions.name] {
		book := bookRows[versions.value(row, "novel_id")]
		if book == nil {
			return domain.ErrNotReady
		}
		revision, err := strconv.ParseInt(versions.value(row, "revision"), 10, 64)
		if err != nil || revision < 1 {
			return domain.ErrNotReady
		}
		if versions.value(row, "revision") == books.value(book, "settings_revision") {

			heads[books.value(book, "id")] = true
		}
	}
	for old, row := range bookRows {
		setup := setupRows[books.value(row, "setup_id")]
		if setup == nil || !heads[old] || books.value(row, "stage") != "WORLD_PLANNING" || drafts.value(setup, "confirmed_novel_id") != old || drafts.value(setup, "actor_id") != books.value(row, "created_by") || drafts.value(setup, "workspace_id") != actor.WorkspaceID {
			return domain.ErrNotReady
		}
		branch, err := recoveredID(ids.Branches, books.value(row, "branch_id"))
		if err != nil || branch == "" {
			return domain.ErrNotReady
		}
		books.set(row, "id", bookIDs[old])
		books.set(row, "setup_id", setupIDs[books.value(row, "setup_id")])
		books.set(row, "branch_id", branch)
		books.set(row, "project_id", ids.TargetProject)
		books.set(row, "created_by", actor.ID)
	}
	for _, table := range []recoveryTable{drafts, versions} {
		for _, row := range data[table.name] {
			if err := remapNovelRecoverySettings(table, row, ids); err != nil {
				return err
			}
			if table.name == drafts.name {
				revision, err := strconv.ParseInt(table.value(row, "revision"), 10, 64)
				step, stepErr := strconv.Atoi(table.value(row, "step"))
				if err != nil || revision < 1 || stepErr != nil || step < 1 || step > 5 {
					return domain.ErrNotReady
				}
				table.set(row, "id", setupIDs[table.value(row, "id")])
				table.set(row, "actor_id", actor.ID)
				table.set(row, "confirmed_novel_id", bookIDs[table.value(row, "confirmed_novel_id")])
			} else {
				table.set(row, "novel_id", bookIDs[table.value(row, "novel_id")])
				table.set(row, "created_by", actor.ID)
			}
		}
	}
	if schema == 38 {
		table := recoveryNovelTables[3]
		for _, row := range data[table.name] {
			if bookIDs[table.value(row, "novel_id")] == "" {
				return domain.ErrNotReady
			}
			if err := remapNovelRecoverySettings(table, row, ids); err != nil {
				return err
			}
			table.set(row, "novel_id", bookIDs[table.value(row, "novel_id")])
		}
	}
	for _, table := range tables {
		for _, row := range data[table.name] {
			args := make([]any, len(row))
			for i, cell := range row {
				if cell.Valid {
					args[i] = cell.String
				}
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO "+table.name+"("+table.columns+") VALUES("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...); err != nil {
				return err
			}
		}
	}
	// Resolve the complete linked graph only after all three kinds are inserted.
	for _, table := range tables {
		for _, row := range data[table.name] {
			primary := map[string]string{"id": table.value(row, "id")}
			if table.name == versions.name {
				primary = map[string]string{"novel_id": table.value(row, "novel_id"), "revision": table.value(row, "revision")}
			}
			if table.name == "novel_settings_drafts" {
				primary = map[string]string{"novel_id": table.value(row, "novel_id"), "actor_id": table.value(row, "actor_id")}
			}
			if err := s.captureSourceValuesTx(ctx, tx, ids.TargetProject, retention.SourceKey{Table: table.name, Primary: primary}); err != nil {
				return err
			}
		}
	}
	return nil
}

func remapNovelRecoverySettings(table recoveryTable, row recoveryRow, ids RecoveryIPIDs) error {
	raw := table.value(row, "payload")
	var setup novel.Setup
	if digest(raw) != table.value(row, "content_hash") || json.Unmarshal([]byte(raw), &setup) != nil || setup.Validate(table.name != "novel_settings_drafts") != nil {
		return domain.ErrNotReady
	}
	if setup.IPMode == "existing" {
		if setup.ProjectID != ids.SourceProject {
			return domain.ErrNotReady
		}
		branch, err := recoveredID(ids.Branches, setup.BranchID)
		if err != nil {
			return err
		}
		head, err := recoveredID(ids.Revisions, setup.BaseHead)
		if err != nil {
			return err
		}
		setup.ProjectID, setup.BranchID, setup.BaseHead = ids.TargetProject, branch, head
	}
	encoded, err := json.Marshal(setup)
	if err != nil {
		return err
	}
	table.set(row, "payload", string(encoded))
	table.set(row, "content_hash", digest(string(encoded)))
	return nil
}
