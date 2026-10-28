package backup

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/memory"
	"novelstudio/internal/storage"
)

func TestMemoryMySQLMigrationSnapshotRestore(t *testing.T) {
	db, cfg, admin := testDatabase(t)
	ctx := context.Background()
	if err := storage.RunMigrations(ctx, db); err != nil {
		t.Fatal("migration replay", err)
	}
	if err := storage.CheckVersion(ctx, db); err != nil {
		t.Fatal("migration journal", err)
	}
	a := memoryArchiveFixture(t)
	testRow(a, "operations", map[string]string{"actor_id": strings.Repeat("4", 32), "operation_id": "request-v2:" + strings.Repeat("d", 32), "kind": "memory.session", "request_hash": strings.Repeat("e", 64), "result_id": strings.Repeat("9", 32), "created_at": "2026-10-25 13:00:00"})
	items, revisions := memorySpec("memory_items"), memorySpec("memory_revisions")
	var current memory.Item
	_ = json.Unmarshal([]byte(items.value(a.Data[items.name][0], "document")), &current)
	// Preserve the private original and add an adopted immutable head. The
	// historical release remains pinned after the resource head is withdrawn.
	prior := current.Revision
	current.Revision = strings.Repeat("c", 32)
	current.Actor, current.Session, current.State = "", "", "published"
	current.Summary = &memory.Summary{Level: "chapter", UnitID: strings.Repeat("1", 32), Title: "第一章"}
	raw, _ := json.Marshal(current)
	items.set(a.Data[items.name][0], "document", string(raw))
	items.set(a.Data[items.name][0], "head_revision", current.Revision)
	items.set(a.Data[items.name][0], "state", current.State)
	a.Data[items.name][0][items.index("actor_id")] = nil
	a.Data[items.name][0][items.index("session_id")] = nil
	testRow(a, revisions.name, map[string]string{"id": current.Revision, "project_id": current.Project, "item_id": current.ID, "parent_id": prior, "document": string(raw), "actor_id": strings.Repeat("4", 32), "created_at": "2026-10-25 13:00:00"})
	volume := current
	volume.ID, volume.Revision, volume.Text, volume.Sources = strings.Repeat("e", 32), strings.Repeat("f", 32), "第一卷摘要", nil
	volume.Summary = &memory.Summary{Level: "volume", UnitID: strings.Repeat("0", 32), Title: "第一卷", Inputs: []memory.SummaryPin{{ID: current.ID, Revision: current.Revision, Hash: memory.SummaryTextHash(current.Text)}}}
	volumeRaw, _ := json.Marshal(volume)
	testRow(a, items.name, map[string]string{"id": volume.ID, "project_id": volume.Project, "branch_id": volume.Branch, "head_revision": volume.Revision, "kind": volume.Kind, "state": volume.State, "document": string(volumeRaw), "created_at": "2026-10-25 13:00:00", "updated_at": "2026-10-25 13:00:00"})
	testRow(a, revisions.name, map[string]string{"id": volume.Revision, "project_id": volume.Project, "item_id": volume.ID, "document": string(volumeRaw), "actor_id": strings.Repeat("4", 32), "created_at": "2026-10-25 13:00:00"})
	resource := memorySpec("resources")
	resource.set(a.Data[resource.name][0], "head_revision", "")
	resource.set(a.Data[resource.name][0], "content_hash", "")
	for _, spec := range tableCatalog {
		for _, row := range a.Data[spec.name] {
			values := make([]any, len(row))
			for i, v := range row {
				if v != nil {
					values[i] = string(v)
				}
			}
			execTest(t, db, "INSERT INTO "+spec.name+" ("+spec.columns+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(row)), ",")+")", values...)
		}
	}
	if _, err := db.Exec("UPDATE memory_sessions SET revision=0"); err == nil {
		t.Fatal("invalid checkpoint revision accepted")
	}
	root := t.TempDir()
	blobs := storage.LocalBlobs{Root: filepath.Join(root, "source")}
	for name, body := range a.Blobs {
		parts := strings.Split(name, "/")
		if _, err := blobs.PutObject(ctx, parts[1], bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	ledger := provisionTestAuthority(t, db, admin)
	archive := filepath.Join(root, "memory.zip")
	if _, err := Create(ctx, db, blobs, archive); err != nil {
		t.Fatal("snapshot", err)
	}
	opt := RestoreOptions{ArchivePath: archive, TargetDSN: targetDSN(t, cfg, admin), BlobRoot: filepath.Join(root, "restored"), Authority: ProtectedAuthority{DB: admin, Ledger: ledger}}
	if _, err := Restore(ctx, opt); err != nil {
		t.Fatal("restore", err)
	}
	target, err := sql.Open("mysql", opt.TargetDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	var n int
	if err := target.QueryRow("SELECT COUNT(*) FROM operations WHERE kind='memory.session' AND result_id=?", strings.Repeat("9", 32)).Scan(&n); err != nil || n != 1 {
		t.Fatal("session receipt lost in restore", n, err)
	}
	for _, q := range []string{"SELECT COUNT(*) FROM memory_revisions", "SELECT COUNT(*) FROM memory_items WHERE actor_id IS NULL AND session_id IS NULL", "SELECT COUNT(*) FROM memory_sessions WHERE JSON_UNQUOTE(JSON_EXTRACT(document,'$.notes'))='私有推理' AND revision=3", "SELECT COUNT(*) FROM users WHERE enabled=0 AND password_hash=''"} {
		want := 1
		if strings.Contains(q, "FROM memory_revisions") {
			want = 3
		} else if strings.Contains(q, "FROM memory_items") {
			want = 2
		}
		if err := target.QueryRow(q).Scan(&n); err != nil || n != want {
			t.Fatal("memory restore mismatch", q, n, err)
		}
	}
	var restoredRaw []byte
	if err := target.QueryRow("SELECT document FROM memory_items WHERE id=?", volume.ID).Scan(&restoredRaw); err != nil {
		t.Fatal(err)
	}
	var restoredVolume memory.Item
	if json.Unmarshal(restoredRaw, &restoredVolume) != nil || restoredVolume.Summary == nil || len(restoredVolume.Summary.Inputs) != 1 || restoredVolume.Summary.Inputs[0] != volume.Summary.Inputs[0] {
		t.Fatal("restored summary lost exact historical source")
	}
	for name, body := range a.Blobs {
		parts := strings.Split(name, "/")
		got, err := (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, parts[1], parts[2])
		if err != nil || !bytes.Equal(got, body) {
			t.Fatal("pinned source lost", err)
		}
	}
	// Object-anchor corruption must fail before publishing a snapshot file.
	current.Sources[0].End = 7
	raw, _ = json.Marshal(current)
	execTest(t, db, "UPDATE memory_items SET document=? WHERE id=?", string(raw), current.ID)
	execTest(t, db, "UPDATE memory_revisions SET document=? WHERE id=?", string(raw), current.Revision)
	rejected := filepath.Join(root, "corrupt.zip")
	if _, err := Create(ctx, db, blobs, rejected); err == nil {
		t.Fatal("out of bounds source archived")
	}
	if _, err := os.Stat(rejected); !os.IsNotExist(err) {
		t.Fatal("failed snapshot published file", err)
	}
}
