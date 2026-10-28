package backup

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
)

func beforeMemory(a *Archive) {
	for _, name := range memoryTables {
		delete(a.Data, name)
	}
}

func memoryArchiveFixture(t *testing.T) *Archive {
	t.Helper()
	a := emptyArchive(t)
	id := func(v string) string { return strings.Repeat(v, 32) }
	stamp := "2026-10-25 12:00:00"
	body := []byte("原文甲乙丙丁")
	manifest := domain.Manifest{Schema: 1, BranchID: id("5"), Artifacts: []domain.ArtifactRef{{ResourceID: id("a"), Hash: digest(body), Size: int64(len(body))}}}
	manifestRaw, _ := json.Marshal(manifest)
	item := memory.Item{ID: id("7"), Revision: id("8"), Project: id("2"), Branch: id("5"), Actor: id("4"), Session: id("9"), Kind: "summary", State: "candidate", Visibility: "author", Text: "私有摘要", Sources: []memory.Source{{Resource: id("a"), Revision: id("b"), Hash: digest(body), Start: 0, End: 6}}}
	itemRaw, _ := json.Marshal(item)
	testRow(a, "workspaces", map[string]string{"id": id("1"), "name": "memory test", "created_at": stamp})
	testRow(a, "users", map[string]string{"id": id("4"), "workspace_id": id("1"), "username": "memory-owner", "password_hash": "", "role": "owner", "enabled": "0", "created_at": stamp})
	testRow(a, "projects", map[string]string{"id": id("2"), "workspace_id": id("1"), "name": "novel", "revision": "1", "created_at": stamp})
	testRow(a, "branches", map[string]string{"id": id("5"), "project_id": id("2"), "name": "main", "parent_id": "", "base_revision": "", "head_revision": "", "revision": "1"})
	testRow(a, "resources", map[string]string{"id": id("a"), "project_id": id("2"), "branch_id": id("5"), "name": "source", "kind": "source", "head_revision": id("b"), "content_hash": digest(body), "revision": "1", "created_at": stamp})
	testRow(a, "artifact_releases", map[string]string{"id": id("b"), "project_id": id("2"), "branch_id": id("5"), "manifest": string(manifestRaw), "content_hash": digest(manifestRaw), "status": "PUBLISHED", "created_by": id("4"), "adopted_by": id("4"), "adopted_hash": digest(manifestRaw), "created_at": stamp})
	testRow(a, "memory_sessions", map[string]string{"id": id("9"), "project_id": id("2"), "branch_id": id("5"), "actor_id": id("4"), "revision": "3", "document": `{"title":"写作","goal":"继续","notes":"私有推理","focus_ids":["` + id("7") + `"]}`, "created_at": stamp, "updated_at": stamp})
	testRow(a, "memory_items", map[string]string{"id": id("7"), "project_id": id("2"), "branch_id": id("5"), "head_revision": id("8"), "kind": "summary", "state": "candidate", "actor_id": id("4"), "session_id": id("9"), "document": string(itemRaw), "created_at": stamp, "updated_at": stamp})
	testRow(a, "memory_revisions", map[string]string{"id": id("8"), "project_id": id("2"), "item_id": id("7"), "document": string(itemRaw), "actor_id": id("4"), "created_at": stamp})
	a.Blobs["blobs/"+id("2")+"/"+digest(body)] = body
	a.Blobs["blobs/"+id("2")+"/"+digest(manifestRaw)] = manifestRaw
	return a
}

func memorySpec(name string) tableSpec {
	for _, s := range tableCatalog {
		if s.name == name {
			return s
		}
	}
	panic(name)
}

func TestMemoryArchiveRetainsPrivateCheckpointAndRejectsCorruption(t *testing.T) {
	for _, mode := range []string{"valid", "historical-source", "foreign-source", "anchor-overflow", "head-diverged", "parent-cycle", "foreign-focus"} {
		t.Run(mode, func(t *testing.T) {
			a := memoryArchiveFixture(t)
			items, revisions := memorySpec("memory_items"), memorySpec("memory_revisions")
			var item memory.Item
			_ = json.Unmarshal([]byte(items.value(a.Data[items.name][0], "document")), &item)
			switch mode {
			case "historical-source":
				s := memorySpec("resources")
				s.set(a.Data[s.name][0], "head_revision", "")
				s.set(a.Data[s.name][0], "content_hash", "")
			case "foreign-source":
				item.Sources[0].Revision = strings.Repeat("f", 32)
			case "anchor-overflow":
				item.Sources[0].End = 7
			case "head-diverged":
				items.set(a.Data[items.name][0], "head_revision", strings.Repeat("e", 32))
			case "parent-cycle":
				revisions.set(a.Data[revisions.name][0], "parent_id", item.Revision)
			case "foreign-focus":
				s := memorySpec("memory_sessions")
				s.set(a.Data[s.name][0], "document", `{"title":"x","goal":"","notes":"","focus_ids":["`+strings.Repeat("f", 32)+`"]}`)
			}
			raw, _ := json.Marshal(item)
			items.set(a.Data[items.name][0], "document", string(raw))
			revisions.set(a.Data[revisions.name][0], "document", string(raw))
			encoded := archiveBytes(t, a)
			read, err := ValidateArchive(bytes.NewReader(encoded), int64(len(encoded)))
			if mode == "valid" || mode == "historical-source" {
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(read.Data["memory_sessions"][0][5], a.Data["memory_sessions"][0][5]) {
					t.Fatal("private checkpoint lost")
				}
			} else if err == nil {
				t.Fatal("corrupt memory archive accepted")
			}
		})
	}
}

func TestMemoryTablesRequireExactReadOnlyGrants(t *testing.T) {
	tables := readerGrantTables("studio_next_test", "authority_test")
	for _, name := range memoryTables {
		found := false
		for _, table := range tables {
			if table.schema == "studio_next_test" && table.name == name {
				found = true
			}
		}
		if !found {
			t.Fatal("missing exact reader grant", name)
		}
	}
}

func TestMemoryContextPreservesPinsAndAuthority(t *testing.T) {
	for _, mode := range []string{"valid", "changed-text", "foreign-actor", "changed-channel", "invalid-json"} {
		t.Run(mode, func(t *testing.T) {
			a := memoryArchiveFixture(t)
			spec := memorySpec("memory_items")
			var item memory.Item
			_ = json.Unmarshal([]byte(spec.value(a.Data[spec.name][0], "document")), &item)
			item.Score = 2.5
			r := memory.Request{Scope: memory.Scope{Project: item.Project, Branch: item.Branch, Canon: "empty", Actor: item.Actor, Session: item.Session, Timeline: "main", Purpose: "author", PermissionEpoch: "epoch"}, Budget: 8192, SourceEpoch: "epoch", CompilerVersion: memory.Version}
			pack, err := memory.Compile(r, []memory.Item{item})
			if err != nil || len(pack.Items) != 1 {
				t.Fatal("fixture compile", err)
			}
			switch mode {
			case "changed-text":
				pack.Items[0].Item.Text = "伪造权威"
			case "foreign-actor":
				pack.Request.Scope.Actor = strings.Repeat("f", 32)
			case "changed-channel":
				pack.Items[0].Channel = "canon"
			}
			pack.Hash = ""
			raw, _ := json.Marshal(pack)
			pack.Hash = digest(raw)
			payload, _ := json.Marshal(map[string]any{"memory": pack, "existing_context_field": "preserved"})
			if mode == "invalid-json" {
				payload = []byte(`{"memory":1}`)
			}
			testRow(a, "context_packages", map[string]string{"id": strings.Repeat("d", 64), "project_id": item.Project, "branch_id": item.Branch, "actor_id": item.Actor, "payload": string(payload)})
			err = validateMemoryContexts(a.Data)
			if (mode == "valid") != (err == nil) {
				t.Fatal("unexpected context validation", mode, err)
			}
		})
	}
}

func TestMemorySchema35RegistersExactColumnsAndLegacyBoundary(t *testing.T) {
	want := map[string]string{
		"memory_sessions":  "id,project_id,branch_id,actor_id,revision,document,created_at,updated_at",
		"memory_items":     "id,project_id,branch_id,head_revision,kind,state,actor_id,session_id,document,created_at,updated_at",
		"memory_revisions": "id,project_id,item_id,parent_id,document,actor_id,created_at",
	}
	for _, spec := range tableCatalog {
		if columns, exists := want[spec.name]; exists {
			if spec.columns != columns {
				t.Fatal("memory column contract differs", spec.name)
			}
			delete(want, spec.name)
		}
	}
	if supportedSchemaVersion != 35 || len(want) != 0 {
		t.Fatal("memory schema not independently registered", supportedSchemaVersion, want)
	}
	a := emptyArchive(t)
	a.Manifest.Schema = 34
	a.Manifest.Migrations = a.Manifest.Migrations[:34]
	raw := archiveBytes(t, a)
	if _, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw))); err == nil {
		t.Fatal("future memory tables accepted in schema34")
	}
	for _, name := range []string{"memory_sessions", "memory_items", "memory_revisions"} {
		delete(a.Data, name)
	}
	raw = archiveBytes(t, a)
	read, err := ValidateArchive(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"memory_sessions", "memory_items", "memory_revisions"} {
		if rows, exists := read.Data[name]; !exists || len(rows) != 0 {
			t.Fatal("legacy memory conversion not empty", name)
		}
	}
}
