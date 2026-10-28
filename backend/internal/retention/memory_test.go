package retention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
)

func TestMemorySessionOperationOwnership(t *testing.T) {
	rows := memoryOwnershipFixture(t, "candidate")
	id := func(s string) string { return strings.Repeat(s, 32) }
	operation := inventoryRow("operations", map[string]string{"actor_id": id("4"), "operation_id": "request-v2:" + id("c"), "kind": "memory.session", "request_hash": strings.Repeat("d", 64), "result_id": id("9")})
	rows = append(rows, operation)
	index, err := NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := index.Owner(operation.Table, operation.Values)
	if err != nil || owner.ProjectID != id("2") {
		t.Fatal("session receipt lost project ownership", owner, err)
	}
	operation.Values["result_id"] = id("f")
	index, err = NewSourceIndex(rows)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = index.Owner(operation.Table, operation.Values); err == nil {
		t.Fatal("orphan receipt accepted")
	}
}

func TestMemorySummaryOwnershipPinsHistoricalChild(t *testing.T) {
	id := func(s string) string { return strings.Repeat(s, 32) }
	for _, bad := range []string{"", "foreign-project", "wrong-hash", "wrong-level"} {
		t.Run(bad, func(t *testing.T) {
			rows := memoryOwnershipFixture(t, "published")
			var child memory.Item
			for i := range rows {
				if rows[i].Table == "memory_items" || rows[i].Table == "memory_revisions" {
					if err := json.Unmarshal([]byte(rows[i].Values["document"]), &child); err != nil {
						t.Fatal(err)
					}
					child.Summary = &memory.Summary{Level: "chapter", UnitID: id("6"), Title: "第一章"}
					raw, _ := json.Marshal(child)
					rows[i].Values["document"] = string(raw)
				}
			}
			parent := child
			parent.ID, parent.Revision, parent.Sources = id("c"), id("d"), nil
			parent.Summary = &memory.Summary{Level: "volume", UnitID: id("e"), Title: "第一卷", Inputs: []memory.SummaryPin{{ID: child.ID, Revision: child.Revision, Hash: memory.SummaryTextHash(child.Text)}}}
			switch bad {
			case "foreign-project":
				parent.Project = id("f")
			case "wrong-hash":
				parent.Summary.Inputs[0].Hash = strings.Repeat("f", 64)
			case "wrong-level":
				parent.Summary.Level = "book"
			}
			raw, _ := json.Marshal(parent)
			parentRow := inventoryRow("memory_revisions", map[string]string{"id": parent.Revision, "project_id": id("2"), "item_id": parent.ID, "document": string(raw), "actor_id": id("4")})
			rows = append(rows, inventoryRow("memory_items", map[string]string{"id": parent.ID, "project_id": id("2"), "branch_id": id("5"), "head_revision": parent.Revision, "kind": "summary", "state": "published", "document": string(raw)}), parentRow)
			index, err := NewSourceIndex(rows)
			if err != nil {
				t.Fatal(err)
			}
			_, err = index.Owner(parentRow.Table, parentRow.Values)
			if (err == nil) != (bad == "") {
				t.Fatalf("%s: %v", bad, err)
			}
		})
	}
}

func memoryOwnershipFixture(t *testing.T, state string) []SourceRow {
	t.Helper()
	id := func(s string) string { return strings.Repeat(s, 32) }
	hash := func(raw []byte) string { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
	body := []byte("记忆来源原文")
	manifest := domain.Manifest{Schema: 1, BranchID: id("5"), Artifacts: []domain.ArtifactRef{{ResourceID: id("a"), Hash: hash(body), Size: int64(len(body))}}}
	manifestJSON, _ := json.Marshal(manifest)
	item := memory.Item{ID: id("7"), Revision: id("8"), Project: id("2"), Branch: id("5"), Actor: id("4"), Session: id("9"), Kind: "summary", State: state, Visibility: "author", Text: "作者摘要", Sources: []memory.Source{{Resource: id("a"), Revision: id("b"), Hash: hash(body), Start: 0, End: 6}}}
	if state == "published" || state == "adopted_plan" {
		item.Actor, item.Session = "", ""
	}
	raw, _ := json.Marshal(item)
	rows := sourceOwnershipRows()
	rows = append(rows,
		inventoryRow("resources", map[string]string{"id": id("a"), "project_id": id("2"), "branch_id": id("5"), "head_revision": id("b"), "content_hash": hash(body)}),
		inventoryRow("artifact_releases", map[string]string{"id": id("b"), "project_id": id("2"), "branch_id": id("5"), "manifest": string(manifestJSON), "content_hash": hash(manifestJSON), "status": "PUBLISHED", "created_by": id("4"), "adopted_by": id("4"), "adopted_hash": hash(manifestJSON)}),
		inventoryRow("memory_sessions", map[string]string{"id": id("9"), "project_id": id("2"), "branch_id": id("5"), "actor_id": id("4"), "revision": "1", "document": `{"title":"继续写","goal":"写完当前章节","notes":"私有推理","focus_ids":["` + id("7") + `"]}`}),
		inventoryRow("memory_items", map[string]string{"id": id("7"), "project_id": id("2"), "branch_id": id("5"), "head_revision": id("8"), "kind": item.Kind, "state": state, "actor_id": item.Actor, "session_id": item.Session, "document": string(raw)}),
		inventoryRow("memory_revisions", map[string]string{"id": id("8"), "project_id": id("2"), "item_id": id("7"), "document": string(raw), "actor_id": id("4")}),
	)
	return rows
}

func TestMemoryOwnershipValidatesHistoryAndPrivateBackPointers(t *testing.T) {
	for _, state := range []string{"candidate", "published", "adopted_plan"} {
		rows := memoryOwnershipFixture(t, state)
		index, err := NewSourceIndex(rows)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if strings.HasPrefix(row.Table, "memory_") {
				if _, err := index.Owner(row.Table, row.Values); err != nil {
					t.Fatal(state, row.Table, err)
				}
			}
		}
		refs, err := index.AdoptedContentReferences(strings.Repeat("2", 32))
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, ref := range refs {
			if strings.HasPrefix(ref.Table, "memory_") {
				count++
			}
		}
		want := 2
		if state == "candidate" {
			want = 0
		}
		if count != want {
			t.Fatal("memory promotion count", state, count, want)
		}
		for _, key := range []string{"memory_sessions.document"} {
			rule, _ := Rule(key)
			if rule.Promotable || rule.Class != RawRun {
				t.Fatal("private notes promoted", key)
			}
		}
	}
}

func TestMemoryOwnershipRejectsCrossScopeAndCorruptPins(t *testing.T) {
	for _, mode := range []string{"foreign-project", "foreign-branch", "foreign-session", "source-hash", "missing-head", "document-owner", "parent-cycle", "unknown-json", "focus-foreign", "duplicate-json", "missing-checkpoint-fields", "null-focus", "duplicate-focus"} {
		t.Run(mode, func(t *testing.T) {
			rows := memoryOwnershipFixture(t, "candidate")
			item, revision, session := &rows[len(rows)-2], &rows[len(rows)-1], &rows[len(rows)-3]
			target := item
			switch mode {
			case "foreign-project":
				item.Values["project_id"] = strings.Repeat("3", 32)
			case "foreign-branch":
				item.Values["branch_id"] = strings.Repeat("6", 32)
			case "foreign-session":
				session.Values["branch_id"] = strings.Repeat("6", 32)
			case "source-hash":
				var doc memory.Item
				_ = json.Unmarshal([]byte(item.Values["document"]), &doc)
				doc.Sources[0].Hash = strings.Repeat("f", 64)
				raw, _ := json.Marshal(doc)
				item.Values["document"], revision.Values["document"] = string(raw), string(raw)
			case "missing-head":
				item.Values["head_revision"] = strings.Repeat("e", 32)
			case "document-owner":
				item.Values["document"] = strings.Replace(item.Values["document"], `"project":"`+strings.Repeat("2", 32), `"project":"`+strings.Repeat("3", 32), 1)
			case "parent-cycle":
				revision.Values["parent_id"] = revision.Values["id"]
				target = revision
			case "unknown-json":
				session.Values["document"] = `{"title":"x","secret_extra":"unexpected"}`
				target = session
			case "focus-foreign":
				session.Values["document"] = `{"title":"x","goal":"","notes":"","focus_ids":["` + strings.Repeat("e", 32) + `"]}`
				target = session
			case "duplicate-json":
				item.Values["document"] = strings.Replace(item.Values["document"], `"kind":"summary"`, `"kind":"fact","kind":"summary"`, 1)
			case "missing-checkpoint-fields":
				session.Values["document"] = `{"title":"x","focus_ids":[]}`
				target = session
			case "null-focus":
				session.Values["document"] = `{"title":"x","goal":"","notes":"","focus_ids":null}`
				target = session
			case "duplicate-focus":
				session.Values["document"] = `{"title":"x","goal":"","notes":"","focus_ids":["` + item.Values["id"] + `","` + item.Values["id"] + `"]}`
				target = session
			}
			index, err := NewSourceIndex(rows)
			if err == nil {
				_, err = index.Owner(target.Table, target.Values)
			}
			if err == nil {
				t.Fatal("invalid memory ownership accepted")
			}
		})
	}
}
