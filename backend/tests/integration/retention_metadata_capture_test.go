package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionMetadataWritersCaptureCreationRenameAndRollback(t *testing.T) {
	b, db := setup(t)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	installSourceRegistrationFixture(t, db)
	w, ctx := &service.Workspace{DB: db}, context.Background()
	ip := &service.IP{Workspace: w}
	project, err := w.CreateProject(ctx, actor.ID, "旧书名", "metadata-project")
	if err != nil {
		t.Fatal(err)
	}
	original := capturedVersion(t, db, project.ID, "projects", project.ID, "name")
	branch, err := ip.CreateBranch(ctx, actor, project.ID, "正史", "", "", "metadata-branch")
	if err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project.ID, "branches", branch.ID, "name")
	resource, err := ip.CreateResource(ctx, actor, project.ID, service.ResourceInput{BranchID: branch.ID, Name: "角色资料", Kind: "note", OperationID: "metadata-resource"})
	if err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project.ID, "resources", resource.ID, "name")
	updated, err := w.UpdateProject(ctx, actor.ID, project.ID, "新书名", project.Revision)
	if err != nil {
		t.Fatal(err)
	}
	current := capturedVersion(t, db, project.ID, "projects", project.ID, "name")
	if current.Reference.Revision != original.Reference.Revision+1 || current.Reference.PayloadHash == original.Reference.PayloadHash {
		t.Fatal("rename did not preserve separate source versions", original, current)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_project_name_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='projects.name' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected project name proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_project_name_proof") })
	if _, err := w.UpdateProject(ctx, actor.ID, project.ID, "不应保存的书名", updated.Revision); err == nil {
		t.Fatal("rename committed without source proof")
	}
	var name string
	var revision int64
	if err := db.QueryRow("SELECT name,revision FROM projects WHERE id=?", project.ID).Scan(&name, &revision); err != nil || name != updated.Name || revision != updated.Revision {
		t.Fatal("failed rename changed project", name, revision, err)
	}
	after := capturedVersion(t, db, project.ID, "projects", project.ID, "name")
	if after.Reference.Revision != current.Reference.Revision || !after.Reference.FirstVerifiedAt.Equal(current.Reference.FirstVerifiedAt) {
		t.Fatal("failed rename changed source history", current, after)
	}
}
