package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionWriterCapturePreservesContextWithoutPriorScan(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "保存时固定旧上下文", "capture-context")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "故事草稿")
	b.request("PUT", "/api/projects/"+project+"/resources/"+rid+"/draft", map[string]any{"revision": 0, "text": "旧方案：主角先去寻找姐姐。"}, 200)
	path := "/api/projects/" + project + "/branches/" + bid + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	ref := workingChoice(t, b, path, "resource_draft", rid)
	result := b.request("POST", path+"/search", map[string]any{"query": "寻找姐姐", "purpose": "author", "limit": 1, "include_working": true, "working_refs": []any{ref}}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	installSourceRegistrationFixture(t, db)
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	ctx := context.Background()
	// No operator-wide registration before the edit: the actual save path
	// itself must preserve enough proof for this already saved context.
	if _, err := ip.SaveResourceDraft(ctx, actor, project, rid, service.ResourceDraft{Revision: 1, Text: "新方案：先守住港口。"}); err != nil {
		t.Fatal(err)
	}
	report, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || report.UnboundSources != 0 {
		t.Fatal("old context lost after actual edit", report, err)
	}
	key, err := retention.RowOwnerKey("context_packages", map[string]string{"id": result["id"].(string)})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='context_packages' AND r.owner_key=?", project, key).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &version); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, pin := range version.Reference.Sources {
		var table string
		var sourceRaw string
		if err := db.QueryRow("SELECT r.owner_table,v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=? WHERE r.id=?", pin.Revision, pin.ReferenceID).Scan(&table, &sourceRaw); err != nil {
			t.Fatal(err)
		}
		if table != "resource_drafts" {
			continue
		}
		var source retention.ReferenceVersion
		if err := json.Unmarshal([]byte(sourceRaw), &source); err != nil || source.DraftVersion == nil || source.DraftVersion.Version != "1" {
			t.Fatal("context rebound to new draft", source, err)
		}
		found = true
	}
	if !found {
		t.Fatal("context has no original draft pin")
	}
	if _, err := (&service.Knowledge{IP: ip}).ReadContext(ctx, actor, project, bid, result["id"].(string)); !errors.Is(err, domain.ErrConflict) && !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("history proof incorrectly granted current execution", err)
	}
}

func TestRetentionDraftWritesCaptureOldAndNewValues(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "草稿来源", "capture-project")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "第一章")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip, ctx := &service.IP{Workspace: &service.Workspace{DB: db}}, context.Background()
	old, err := ip.SaveResourceDraft(ctx, actor, project, rid, service.ResourceDraft{Text: "原稿中的旧线索"})
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	next := old
	next.Text = "修改后的新线索"
	if _, err := ip.SaveResourceDraft(ctx, actor, project, rid, next); err != nil {
		t.Fatal(err)
	}
	var first, last string
	var count int
	if err := db.QueryRow("SELECT COUNT(*),MIN(CAST(s.first_verified_at AS CHAR)),MAX(CAST(v.first_verified_at AS CHAR)) FROM retention_source_slots s JOIN retention_source_values v ON v.source_id=s.id WHERE s.project_id=? AND s.owner_table='resource_drafts' AND s.field_key='body'", project).Scan(&count, &first, &last); err != nil || count != 2 {
		t.Fatal("old and new draft observations missing", count, err)
	}
	for _, body := range []string{old.Text, next.Text} {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='resource_drafts' AND v.payload_hash=?", project, hash).Scan(&count); err != nil || count != 1 {
			t.Fatal("capture does not match actual stored text", count, err)
		}
	}
	if _, err := ip.SaveResourceDraft(ctx, actor, project, rid, old); err == nil {
		t.Fatal("stale edit unexpectedly accepted")
	}
	var after string
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR) FROM retention_source_slots WHERE project_id=? AND owner_table='resource_drafts' AND field_key='body'", project).Scan(&after); err != nil || after != first || last < first {
		t.Fatal("observation age reset", first, last, after, err)
	}
	for _, version := range []string{"1", "2"} {
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='resource_drafts' AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.draft_version.version'))=?", project, version).Scan(&count); err != nil || count != 1 {
			t.Fatal("business version proof was not captured", version, count, err)
		}
	}
	// The full collector must agree with the targeted source journal rather
	// than repairing it or appending another version of an unchanged draft.
	if _, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil {
		t.Fatal("full inventory rejected writer evidence", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='resource_drafts'", project).Scan(&count); err != nil || count != 2 {
		t.Fatal("full scan changed unchanged draft history", count, err)
	}
}

func TestRetentionTargetedCaptureIsolationAndRollback(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "定向来源", "capture-owned")
	other := createProject(b, "其他作品", "capture-other")
	bid, otherBranch := branch(b, project, "正史"), branch(b, other, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	memory := &service.Memory{IP: &service.IP{Workspace: &service.Workspace{DB: db}}}
	ctx := context.Background()
	session, err := memory.SaveSession(ctx, actor, project, bid, service.MemorySessionInput{OperationID: "session-a", Document: service.MemorySessionDocument{Title: "本书笔记", Notes: "原始主线"}})
	if err != nil {
		t.Fatal(err)
	}
	foreign, err := memory.SaveSession(ctx, actor, other, otherBranch, service.MemorySessionInput{OperationID: "session-b", Document: service.MemorySessionDocument{Title: "另一本书"}})
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	// An unrelated damaged source must not cause a whole-workspace scan on save.
	if _, err := db.Exec("UPDATE memory_sessions SET document='{\"unknown\":true}' WHERE id=?", foreign.ID); err != nil {
		t.Fatal(err)
	}
	target := retention.SourceKey{Table: "memory_sessions", Primary: map[string]string{"id": session.ID}}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	begin := func() *sql.Tx {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { tx.Rollback() })
		return tx
	}
	tx := begin()
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, target); err != nil {
		t.Fatal("targeted capture inspected unrelated source", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_slots").Scan(&count); err != nil || count != 0 {
		t.Fatal("capture escaped rollback", count, err)
	}
	missing := map[string]string{"id": service.NewID()}
	tx = begin()
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project,
		retention.SourceKey{Table: "memory_sessions", Primary: missing, IfPresent: true},
		retention.SourceKey{Table: "memory_sessions", Primary: missing},
	); err == nil {
		t.Fatal("optional duplicate bypassed required source existence")
	}
	tx.Rollback()
	tx = begin()
	if err := retention.CaptureSourceValues(ctx, tx, ledger, other, target); err == nil {
		t.Fatal("cross-project source captured")
	}
	tx.Rollback()
	if _, err := db.Exec("UPDATE memory_sessions SET branch_id=? WHERE id=?", otherBranch, session.ID); err != nil {
		t.Fatal(err)
	}
	tx = begin()
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, target); err == nil {
		t.Fatal("conflicting secondary ownership accepted")
	}
	tx.Rollback()
	if _, err := db.Exec("UPDATE memory_sessions SET branch_id=? WHERE id=?", bid, session.ID); err != nil {
		t.Fatal(err)
	}
	changed := session.Document
	changed.Notes = "修改后的主线"
	if _, err := memory.SaveSession(ctx, actor, project, bid, service.MemorySessionInput{ID: session.ID, ExpectedRevision: session.Revision, OperationID: "session-edit", Document: changed}); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values v JOIN retention_source_slots s ON s.id=v.source_id WHERE s.project_id=? AND s.owner_table='memory_sessions' AND s.field_key='document'", project).Scan(&count); err != nil || count != 2 {
		t.Fatal("session edit lost original observation", count, err)
	}
	for _, version := range []string{"1", "2"} {
		if err := db.QueryRow("SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='memory_sessions' AND JSON_UNQUOTE(JSON_EXTRACT(v.snapshot,'$.working_projection.version'))=?", project, version).Scan(&count); err != nil || count != 1 {
			t.Fatal("session projection proof missing", version, count, err)
		}
	}
	if _, err := db.Exec("UPDATE project_lifecycle SET state='CURRENT',scanned_at=UTC_TIMESTAMP(6) WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	tx = begin()
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, target); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var current int64
	var state string
	var unscanned bool
	if err := db.QueryRow("SELECT inventory_revision,state,scanned_at IS NULL FROM project_lifecycle WHERE project_id=?", project).Scan(&current, &state, &unscanned); err != nil || current <= revision || state != "UNSCANNED" || !unscanned {
		t.Fatal("old cleanup scan survived a capture", current, revision, state, unscanned, err)
	}
}
