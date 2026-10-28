package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
)

var recoveryMemoryTables = []recoveryTable{
	recoverySessionTable,
	{"memory_items", "id,project_id,branch_id,head_revision,kind,state,actor_id,session_id,document,created_at,updated_at", "", "v.project_id"},
	{"memory_revisions", "id,project_id,item_id,parent_id,document,actor_id,created_at", "", "v.project_id"},
}

func readRecoveryMemoryRows(ctx context.Context, tx *sql.Tx, project string) (map[string][]recoveryRow, *recoveryExpiryRows, error) {
	data := map[string][]recoveryRow{}
	total := 0
	for _, table := range recoveryMemoryTables {
		rows, err := readRecoveryTable(ctx, tx, table, project, &total)
		if err != nil {
			return nil, nil, err
		}
		if total > 64<<20 {
			return nil, nil, domain.ErrNotReady
		}
		data[table.name] = rows
	}
	expiry, err := prepareRecoveryExpiryRows(ctx, tx, project, recoveryMemoryTables, data)
	return data, expiry, err
}

func readRecoveryMemoryTx(ctx context.Context, tx *sql.Tx, project string) (memory.RecoveryGraph, map[string]recoveryMemoryDates, error) {
	data, expiry, err := readRecoveryMemoryRows(ctx, tx, project)
	if err != nil {
		return memory.RecoveryGraph{}, nil, err
	}
	return parseRecoveryMemoryRows(project, data, expiry)
}

func parseRecoveryMemoryRows(project string, data map[string][]recoveryRow, expiry *recoveryExpiryRows) (graph memory.RecoveryGraph, dates map[string]recoveryMemoryDates, err error) {
	dates = map[string]recoveryMemoryDates{}
	for _, table := range recoveryMemoryTables {
		for _, row := range data[table.name] {
			v := func(field string) string { return table.value(row, field) }
			proof, expired := expiry.field(row, table, "document")
			raw := []byte(v("document"))
			switch table.name {
			case "memory_sessions":
				revision, e := strconv.ParseInt(v("revision"), 10, 64)
				if e != nil || revision < 1 {
					return graph, dates, domain.ErrNotReady
				}
				var document MemorySessionDocument
				if !expired && decodeRecoveryDocument(raw, &document) != nil {
					return graph, dates, domain.ErrNotReady
				}
				graph.Sessions = append(graph.Sessions, memory.RecoverySession{Expired: expired, ID: v("id"), Project: project, Branch: v("branch_id"), Actor: v("actor_id"), Revision: revision, Title: document.Title, Goal: document.Goal, Notes: document.Notes, FocusIDs: document.FocusIDs})
				dates["session:"+v("id")] = recoveryMemoryDates{created: v("created_at"), updated: v("updated_at"), actor: v("actor_id")}
			case "memory_items":
				if expired {
					graph.ExpiredHeads = append(graph.ExpiredHeads, memory.RecoveryHeadIdentity{ID: v("id"), Project: project, Branch: v("branch_id"), Revision: v("head_revision"), Kind: v("kind"), State: v("state"), Actor: v("actor_id"), Session: v("session_id"), ContentHash: proof.Summary.InputHash})
				} else {
					var item memory.Item
					if decodeRecoveryDocument(raw, &item) != nil || item.Project != project || item.ID != v("id") || item.Branch != v("branch_id") || item.Revision != v("head_revision") || item.Kind != v("kind") || item.State != v("state") || item.Actor != v("actor_id") || item.Session != v("session_id") {
						return graph, dates, domain.ErrNotReady
					}
					graph.Heads = append(graph.Heads, item)
				}
				dates["item:"+v("id")] = recoveryMemoryDates{created: v("created_at"), updated: v("updated_at"), actor: v("actor_id")}
			case "memory_revisions":
				if expired {
					graph.ExpiredRevisions = append(graph.ExpiredRevisions, memory.RecoveryRevisionIdentity{ID: v("id"), Project: project, ItemID: v("item_id"), Parent: v("parent_id"), ContentHash: proof.Summary.InputHash})
				} else {
					var item memory.Item
					if decodeRecoveryDocument(raw, &item) != nil || item.Project != project || item.ID != v("item_id") || item.Revision != v("id") {
						return graph, dates, domain.ErrNotReady
					}
					graph.Revisions = append(graph.Revisions, memory.RecoveryRevision{Item: item, Parent: v("parent_id")})
				}
				dates["revision:"+v("id")] = recoveryMemoryDates{created: v("created_at"), actor: v("actor_id")}
			}
		}
	}
	return graph, dates, nil
}

func remapRecoveryMemoryRows(data map[string][]recoveryRow, expiry *recoveryExpiryRows, copy memory.RecoveryGraph, ids memory.RecoveryMap) (retention.RecoveryExpiryMap, retention.RecoveryExpiryGraph, error) {
	documents := map[string][]byte{}
	put := func(table, id string, value any) error {
		raw, err := json.Marshal(value)
		if err == nil {
			documents[table+":"+id] = raw
		}
		return err
	}
	for _, session := range copy.Sessions {
		if !session.Expired {
			if err := put("memory_sessions", session.ID, MemorySessionDocument{Title: session.Title, Goal: session.Goal, Notes: session.Notes, FocusIDs: session.FocusIDs}); err != nil {
				return retention.RecoveryExpiryMap{}, retention.RecoveryExpiryGraph{}, err
			}
		}
	}
	for _, item := range copy.Heads {
		if err := put("memory_items", item.ID, item); err != nil {
			return retention.RecoveryExpiryMap{}, retention.RecoveryExpiryGraph{}, err
		}
	}
	for _, version := range copy.Revisions {
		if err := put("memory_revisions", version.Item.Revision, version.Item); err != nil {
			return retention.RecoveryExpiryMap{}, retention.RecoveryExpiryGraph{}, err
		}
	}
	for _, table := range recoveryMemoryTables {
		for _, row := range data[table.name] {
			expired := expiry.expired(table.name, row, table, "document")
			for _, column := range strings.Split(table.columns, ",") {
				var mapping map[string]string
				switch column {
				case "project_id":
					table.set(row, column, ids.TargetProject)
					continue
				case "id":
					mapping = map[string]map[string]string{"memory_sessions": ids.Sessions, "memory_items": ids.Items, "memory_revisions": ids.Revisions}[table.name]
				case "branch_id":
					mapping = ids.Branches
				case "head_revision", "parent_id":
					mapping = ids.Revisions
				case "item_id":
					mapping = ids.Items
				case "session_id":
					mapping = ids.Sessions
				}
				if mapping != nil && table.value(row, column) != "" {
					value, err := recoveredID(mapping, table.value(row, column))
					if err != nil {
						return retention.RecoveryExpiryMap{}, retention.RecoveryExpiryGraph{}, err
					}
					table.set(row, column, value)
				}
			}
			if !expired {
				raw, exists := documents[table.name+":"+table.value(row, "id")]
				if !exists {
					return retention.RecoveryExpiryMap{}, retention.RecoveryExpiryGraph{}, domain.ErrNotReady
				}
				table.set(row, "document", string(raw))
			}
		}
	}
	return expiry.rebind(ids.SourceProject, ids.TargetProject, recoveryMemoryTables, data, retention.RecoveryExpiryMap{})
}
