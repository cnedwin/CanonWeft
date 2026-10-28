package integration

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionEditingDraftsKeepHistoryAndOriginalUpload(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "作者编辑来源", "editing-draft-capture")
	branchID := branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip, ctx := propagationService(t, db), context.Background()
	source, err := ip.ImportSource(ctx, actor, project, branchID, "素材.txt", "editing-import", []byte("作者导入的旧文本"))
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	read := func(table string) retention.ReferenceVersion {
		t.Helper()
		var raw string
		if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table=? AND r.field_key='payload'", project, table).Scan(&raw); err != nil {
			t.Fatal(table, err)
		}
		var version retention.ReferenceVersion
		if err := json.Unmarshal([]byte(raw), &version); err != nil {
			t.Fatal(err)
		}
		return version
	}
	draft, err := ip.ReadImportDraft(ctx, actor, project, branchID, source.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft.Content.Text = "尚未采用的用户修改"
	draft, err = ip.SaveImportDraft(ctx, actor, project, branchID, source.ID, draft)
	if err != nil {
		t.Fatal(err)
	}
	first := read("import_drafts")
	if !first.Reference.ContentRoot || first.DraftVersion == nil || first.DraftVersion.Version != "1" {
		t.Fatal("draft is not retained author work", first)
	}
	for _, field := range []string{"source_hash", "report"} {
		if !capturedVersion(t, db, project, "source_imports", source.ID, field).Reference.ContentRoot {
			t.Fatal("draft original source not retained", field)
		}
	}
	// A later narrow consumer sees the upload, not the private draft that
	// retained it. That limited view cannot revoke or corrupt the content root.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = retention.CaptureSourceValuesWithObjects(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, ip.Blobs.ReadObject, retention.SourceKey{Table: "source_imports", Primary: map[string]string{"id": source.ID}}); err != nil {
		tx.Rollback()
		t.Fatal("narrow recapture of retained upload", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !capturedVersion(t, db, project, "source_imports", source.ID, "report").Reference.ContentRoot {
		t.Fatal("narrow capture removed author retention")
	}
	draft.Content.Text = "第二版仍未采用的用户修改"
	if _, err := ip.SaveImportDraft(ctx, actor, project, branchID, source.ID, draft); err != nil {
		t.Fatal(err)
	}
	if current := read("import_drafts"); current.Reference.Revision != first.Reference.Revision+1 || current.DraftVersion.Version != "2" {
		t.Fatal("draft history missing", current)
	}
	patch := service.PatchDraft{Content: &domain.PatchDraftContent{OperationID: "pending-patch", Group: domain.TextPatchGroup{ID: strings.Repeat("a", 32)}, Snippet: "待润色原文", Replacement: "未完成的新描写"}}
	patch, err = ip.SavePatchDraft(ctx, actor, project, branchID, patch)
	if err != nil {
		t.Fatal(err)
	}
	if !read("patch_drafts").Reference.ContentRoot {
		t.Fatal("incomplete patch draft not retained")
	}
	if _, err := db.Exec("CREATE TRIGGER reject_patch_draft BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='patch_drafts.payload' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected draft proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_patch_draft") })
	patch.Content.Replacement = "不应部分保存的修改"
	if _, err := ip.SavePatchDraft(ctx, actor, project, branchID, patch); err == nil {
		t.Fatal("draft saved without history")
	}
	loaded, err := ip.ReadPatchDraft(ctx, actor, project, branchID)
	if err != nil || loaded.Revision != patch.Revision || loaded.Content.Replacement != "未完成的新描写" {
		t.Fatal("partial draft save", loaded, err)
	}
}
