package integration

import (
	"context"
	"errors"
	"github.com/go-sql-driver/mysql"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestNovelSettingsDraftReviewConfirmAndHistory(t *testing.T) {
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, d.ID, service.NovelConfirmation{Revision: d.Revision, OperationID: "settings-test-create", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/novels/" + book.ID + "/settings"
	invalid := book.Setup
	invalid.SourceText = "不能用设置修改替换原稿来源"
	b.request("PUT", root+"/draft", map[string]any{"revision": 0, "base_revision": 1, "setup": invalid}, 400)
	initial := b.request("GET", root+"/draft", nil, 200)
	if initial["revision"] != float64(0) || initial["base_revision"] != float64(1) {
		t.Fatal("initial settings draft", initial)
	}
	changed := book.Setup
	changed.Title = "雾城新卷"
	changed.Perspective = "第一人称"
	input := map[string]any{"revision": 0, "base_revision": 1, "setup": changed}
	saved := b.request("PUT", root+"/draft", input, 200)
	if saved["revision"] != float64(1) {
		t.Fatal("first draft revision", saved)
	}
	if retry := b.request("PUT", root+"/draft", input, 200); retry["revision"] != saved["revision"] {
		t.Fatal("save retry advanced revision")
	}
	unchanged, err := entry.Book(ctx, actor.ID, book.ID)
	if err != nil || unchanged.SettingsRevision != 1 || unchanged.Setup.Title != book.Setup.Title {
		t.Fatal("draft modified current book", err)
	}
	preview := b.request("POST", root+"/preview", map[string]any{"draft_revision": 1}, 200)
	if len(preview["changes"].([]any)) != 2 || preview["planning_changed"] != true {
		t.Fatal("missing settings differences", preview)
	}
	confirm := map[string]any{"draft_revision": 1, "expected_revision": 1, "preview_hash": preview["hash"], "operation_id": "confirm-settings-once", "accept_settings": false}
	b.request("POST", root+"/confirm", confirm, 400)
	confirm["accept_settings"] = true
	confirmed := b.request("POST", root+"/confirm", confirm, 200)
	if confirmed["settings_revision"] != float64(2) {
		t.Fatal("settings version not advanced", confirmed)
	}
	if retry := b.request("POST", root+"/confirm", confirm, 200); retry["settings_revision"] != float64(2) {
		t.Fatal("lost response duplicated settings version")
	}
	b.request("PUT", root+"/draft", map[string]any{"revision": 1, "base_revision": 1, "setup": book.Setup}, 409)
	history := b.request("GET", root+"/versions", nil, 200)["items"].([]any)
	if len(history) != 2 || history[1].(map[string]any)["setup"].(map[string]any)["title"] != book.Setup.Title {
		t.Fatal("initial version overwritten", history)
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("settings inventory", err)
	}
	if _, err = backup.Create(ctx, db, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}, filepath.Join(t.TempDir(), "settings.studio")); err != nil {
		t.Fatal("settings archive", err)
	}
}

func TestNovelSettingsIsolationStaleReviewAndRollback(t *testing.T) {
	b, db, entry, actor := novelEntryFixture(t)
	ctx := context.Background()
	d, err := entry.SaveDraft(ctx, actor.ID, service.NovelSetupDraft{ID: service.NewID(), Step: 5, Setup: validNovelSetup()})
	if err != nil {
		t.Fatal(err)
	}
	book, err := entry.Confirm(ctx, actor.ID, d.ID, service.NovelConfirmation{Revision: d.Revision, OperationID: "settings-isolation-create", AcceptSettings: true})
	if err != nil {
		t.Fatal(err)
	}
	root := "/api/novels/" + book.ID + "/settings"
	other := b.request("POST", "/api/members", map[string]any{"username": "settings-editor", "password": "settings-editor-password", "role": "editor"}, 201)
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+other["id"].(string), map[string]any{"role": "editor"}, 200)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "settings-editor", "password": "settings-editor-password"}, 200)
	ownerSetup := book.Setup
	ownerSetup.Idea = "作者未采用的私人方向"
	b.request("PUT", root+"/draft", map[string]any{"revision": 0, "base_revision": 1, "setup": ownerSetup}, 200)
	otherDraft := editor.request("GET", root+"/draft", nil, 200)
	if otherDraft["revision"] != float64(0) || otherDraft["setup"].(map[string]any)["idea"] == ownerSetup.Idea {
		t.Fatal("private settings leaked to another editor")
	}
	ownerPreview := b.request("POST", root+"/preview", map[string]any{"draft_revision": 1}, 200)
	changed := book.Setup
	changed.Style = "克制的悬疑叙述"
	editor.request("PUT", root+"/draft", map[string]any{"revision": 0, "base_revision": 1, "setup": changed}, 200)
	preview := editor.request("POST", root+"/preview", map[string]any{"draft_revision": 1}, 200)
	ip := &service.IP{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if _, err = ip.CreateResource(ctx, actor, book.ProjectID, service.ResourceInput{BranchID: book.BranchID, Name: "故事规划笔记", Kind: "note", OperationID: "settings-review-note"}); err != nil {
		t.Fatal(err)
	}
	confirmation := service.NovelSettingsConfirmation{DraftRevision: 1, ExpectedRevision: 1, PreviewHash: preview["hash"].(string), OperationID: "settings-editor-confirm", AcceptSettings: true}
	editor.request("POST", root+"/confirm", confirmation, 409)
	preview = editor.request("POST", root+"/preview", map[string]any{"draft_revision": 1}, 200)
	if len(preview["review_resources"].([]any)) != 1 {
		t.Fatal("new review scope omitted", preview)
	}
	confirmation.PreviewHash = preview["hash"].(string)
	if _, err = db.Exec("CREATE TRIGGER settings_revision_fault BEFORE INSERT ON novel_settings_versions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='settings fault'"); err != nil {
		t.Fatal(err)
	}
	_, err = entry.ConfirmSettings(ctx, other["id"].(string), book.ID, confirmation)
	var injected *mysql.MySQLError
	if !errors.As(err, &injected) || injected.Number != 1644 {
		t.Fatal("wrong failure point", err)
	}
	if _, err = db.Exec("DROP TRIGGER settings_revision_fault"); err != nil {
		t.Fatal(err)
	}
	current, err := entry.Book(ctx, actor.ID, book.ID)
	if err != nil || current.SettingsRevision != 1 {
		t.Fatal("failed settings confirmation changed book", err)
	}
	editor.request("POST", root+"/confirm", confirmation, 200)
	b.request("POST", root+"/confirm", service.NovelSettingsConfirmation{DraftRevision: 1, ExpectedRevision: 1, PreviewHash: ownerPreview["hash"].(string), OperationID: "stale-owner-settings", AcceptSettings: true}, 409)
	retained := b.request("GET", root+"/draft", nil, 200)
	if retained["setup"].(map[string]any)["idea"] != ownerSetup.Idea {
		t.Fatal("conflict discarded author input")
	}
	if _, err = (&service.RetentionScanner{Workspace: entry.Workspace, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}).Scan(ctx, actor.ID, book.ProjectID); err != nil {
		t.Fatal("editor settings source invalid", err)
	}
	b.request("PUT", "/api/projects/"+book.ProjectID+"/members/"+other["id"].(string), map[string]any{"role": "reader"}, 200)
	editor.request("PUT", root+"/draft", map[string]any{"revision": 2, "base_revision": 2, "setup": changed}, 403)
	editor.request("GET", root+"/versions", nil, 200)
	project := b.request("GET", "/api/projects/"+book.ProjectID, nil, 200)
	b.request("DELETE", "/api/projects/"+book.ProjectID, map[string]any{"confirm_name": project["name"], "revision": project["revision"], "operation_id": "delete-settings-book"}, 200)
	copied := b.request("POST", "/api/projects/"+book.ProjectID+"/recover", service.ProjectRecoveryInput{Name: "设置恢复副本", Confirmation: project["name"].(string), Revision: int64(project["revision"].(float64)) + 1, OperationID: "recover-settings-book"}, 201)
	var recovered string
	if err = db.QueryRow("SELECT id FROM novels WHERE project_id=?", copied["id"]).Scan(&recovered); err != nil {
		t.Fatal(err)
	}
	got := b.request("GET", "/api/novels/"+recovered+"/settings/draft", nil, 200)
	if got["base_revision"] != float64(1) || got["setup"].(map[string]any)["idea"] != ownerSetup.Idea {
		t.Fatal("recovery lost unadopted settings", got)
	}
	if _, err = backup.Create(ctx, db, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}, filepath.Join(t.TempDir(), "recovered-settings.studio")); err != nil {
		t.Fatal("settings recovery archive", err)
	}
}
