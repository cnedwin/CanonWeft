package integration

import (
	"context"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoveryRetainsAgedPrivateDraftAndPublishedText(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	name := "旧草稿不阻止小说恢复"
	project := createProject(b, name, "expired-draft-recovery")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "第一章")
	release := prepareRelease(b, project, branchID, "", "", "published-text", []map[string]any{{"resource_id": rid, "text": "正式章节仍然完整"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	if _, err := ip.SaveResourceDraft(ctx, actor, project, rid, service.ResourceDraft{BaseHead: release, Text: "已经放弃的旧草稿"}); err != nil {
		t.Fatal(err)
	}
	// Author drafts are content roots, even when observed many years ago.
	expireTransientRecord(t, db, actor.ID, project, "resource_drafts", "body", rid, 0, 0)
	if err := ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	copy, err := ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: "继续写作的小说", Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
	if err != nil {
		t.Fatal("old draft blocked recovery", err)
	}
	var resourceID, copyBranch string
	if err = db.QueryRow("SELECT id,branch_id FROM resources WHERE project_id=? AND name='第一章'", copy.ID).Scan(&resourceID, &copyBranch); err != nil {
		t.Fatal(err)
	}
	draft, err := ip.ReadResourceDraft(ctx, actor, copy.ID, resourceID)
	if err != nil || draft.Text != "已经放弃的旧草稿" {
		t.Fatal("author draft expired or lost", err)
	}
	private, err := (&service.Knowledge{IP: ip}).PrivateStatus(ctx, actor, copy.ID, copyBranch)
	if err != nil || len(private.Manifest.Sources) != 1 || len(private.Manifest.Omissions) != 0 {
		t.Fatal("retained draft unavailable for author retrieval", private, err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-draft-copy.zip")); err != nil {
		t.Fatal(err)
	}
	resource, err := ip.ReadResource(ctx, actor, copy.ID, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := store.ReadObject(ctx, copy.ID, resource.Hash)
	if err != nil || string(body) != "正式章节仍然完整" {
		t.Fatal("published text changed", err)
	}
}
