package integration

import (
	"context"
	"novelstudio/internal/backup"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPeriodicCleanupResumesOwnPlanAndPreservesManualHold(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "周期清理", "periodic")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: store}
	ctx := context.Background()
	object, err := store.PutObject(ctx, project, strings.NewReader("unreferenced temporary material"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-02 03:04:05.000006' WHERE project_id=? AND object_hash=?", project, object.Hash); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_RETENTION_EXECUTION_ENABLED", "1")
	t.Setenv("STUDIO_RETENTION_AUTO_ENABLED", "0")
	t.Setenv("STUDIO_RETENTION_AUTO_ACTOR", actor)
	step, err := scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.State != "PAUSED" {
		t.Fatal(step, err)
	}
	t.Setenv("STUDIO_RETENTION_AUTO_ENABLED", "1")
	status := b.request("GET", "/api/operations/retention-execution", nil, 200)
	if status["automatic_enabled"] != true {
		t.Fatal("configured maintenance hidden", status)
	}
	manual, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	step, err = scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.State != "HELD" || step.JobID != manual.ID {
		t.Fatal("manual preview taken over", step, err)
	}
	if _, err = scanner.CancelPlannedJob(ctx, actor, project, manual.ID, manual.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER periodic_start_failure BEFORE UPDATE ON retention_jobs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='periodic test failure'"); err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.PeriodicCleanupStep(ctx, ""); err == nil {
		t.Fatal("start failure hidden")
	}
	if _, err = db.Exec("DROP TRIGGER periodic_start_failure"); err != nil {
		t.Fatal(err)
	}
	var pending string
	if err = db.QueryRow("SELECT id FROM retention_jobs WHERE project_id=? AND state='PLANNED'", project).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	step, err = scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.JobID != pending || step.State != "DETACHING" {
		t.Fatal("did not resume same periodic plan", step, err)
	}
	job, err := scanner.ReadJob(ctx, actor, project, pending)
	if err != nil || !job.Automatic {
		t.Fatal("automatic origin lost", job, err)
	}
	for i := 0; i < 8 && step.State != "COMPLETE"; i++ {
		step.State, err = scanner.AdvanceJob(ctx, pending)
		if err != nil {
			t.Fatal(err)
		}
	}
	if step.State != "COMPLETE" {
		t.Fatal(step)
	}
	if _, err = store.ReadObject(ctx, project, object.Hash); err == nil {
		t.Fatal("automatic job did not remove expired file")
	}
	if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "periodic.zip")); err != nil {
		t.Fatal("periodic history is not transportable", err)
	}
	step, err = scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.JobID != pending || step.State != "COMPLETE" {
		t.Fatal("same cycle created another job", step, err)
	}
	page, err := scanner.PeriodicCleanupStep(ctx, project)
	if err != nil || !page.Done {
		t.Fatal("cursor did not finish", page, err)
	}
	foreignWorkspace, foreignProject := service.NewID(), service.NewID()
	if _, err = db.Exec("INSERT INTO workspaces(id,name) VALUES(?,'foreign periodic')", foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO projects(id,workspace_id,name) VALUES(?,?,'foreign')", foreignProject, foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	step, err = scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.ProjectID != project {
		t.Fatal("scheduler crossed workspace", step, err)
	}
	if _, err = db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", actor); err != nil {
		t.Fatal(err)
	}
	if _, err = scanner.PeriodicCleanupStep(ctx, ""); err == nil {
		t.Fatal("revoked owner retained maintenance authority")
	}
	if _, err = db.Exec("UPDATE users SET enabled=TRUE WHERE id=?", actor); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE retention_jobs SET state='BLOCKED',reason_code='EXECUTION_UNAVAILABLE' WHERE id=?", pending); err != nil {
		t.Fatal(err)
	}
	step, err = scanner.PeriodicCleanupStep(ctx, "")
	if err != nil || step.State != "BLOCKED" || step.JobID != pending {
		t.Fatal("blocked job restarted", step, err)
	}
	t.Setenv("STUDIO_RETENTION_AUTO_ACTOR", service.NewID())
	if _, err = scanner.PeriodicCleanupStep(ctx, ""); err == nil {
		t.Fatal("unavailable owner replaced automatically")
	}
}
