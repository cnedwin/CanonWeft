package integration

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionJobRoutesRequireInstalledSchema(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "清理预览入口", "retention-job-routes")
	interruptRetentionMigrationMarker(t, db)
	b.request("POST", "/api/projects/"+project+"/retention-jobs", map[string]any{"operation_id": "request-v2:11111111111111111111111111111111"}, 503)
	b.request("GET", "/api/projects/"+project+"/retention-jobs", nil, 503)
	b.request("GET", "/api/projects/"+project+"/retention-jobs/11111111111111111111111111111111", nil, 503)
	b.request("POST", "/api/projects/"+project+"/retention-jobs/11111111111111111111111111111111/cancel", map[string]any{"plan_hash": strings.Repeat("a", 64)}, 503)
}

func TestRetentionJobDurablePreviewReplayAndCancellation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "持久清理预览", "retention-job-durable")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	ctx := context.Background()
	object, err := blobs.PutObject(ctx, project, strings.NewReader("must remain untouched by planning"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	// A real persisted observation predates this preview. Re-scanning must not
	// renew its age or delete it merely because the saved plan contains it.
	if _, err := db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-02 03:04:05.000006' WHERE project_id=? AND object_hash=?", project, object.Hash); err != nil {
		t.Fatal(err)
	}
	op := "request-v2:" + service.NewID()
	first, err := scanner.PlanJob(ctx, actor, project, op)
	if err != nil || first.ID == "" || first.State != "PLANNED" || !first.CheckpointCurrent || first.RemovableObjects != 1 || first.RestoredDisabled {
		t.Fatal("durable preview failed", first, err)
	}
	var raw string
	if err := db.QueryRow("SELECT plan FROM retention_jobs WHERE id=?", first.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	plan, err := retention.ValidatePlanRecord(raw)
	if err != nil || plan.Hash != first.PlanHash {
		t.Fatal("invalid persisted plan", err)
	}
	// Reconstruct the service, as after a process restart. No in-memory task map.
	restarted := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	replay, err := restarted.PlanJob(ctx, actor, project, op)
	if err != nil || replay != first {
		t.Fatal("retry changed original preview", replay, err)
	}
	read, err := restarted.ReadJob(ctx, actor, project, first.ID)
	if err != nil || read != first {
		t.Fatal("restart lost saved preview", read, err)
	}
	if _, err := scanner.Workspace.UpdateProject(ctx, actor, project, "真实变更", 1); err != nil {
		t.Fatal(err)
	}
	stale, err := restarted.PlanJob(ctx, actor, project, op)
	if err != nil || stale.CheckpointCurrent || stale.PlanHash != first.PlanHash || stale.InventoryRevision != first.InventoryRevision {
		t.Fatal("retry regenerated a stale preview", stale, err)
	}
	second, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil || second.ID == first.ID || second.InventoryRevision <= first.InventoryRevision || !second.CheckpointCurrent {
		t.Fatal("new preview failed", second, err)
	}
	if _, err := scanner.CancelPlannedJob(ctx, actor, project, first.ID, strings.Repeat("a", 64)); err != domain.ErrConflict {
		t.Fatal("wrong preview hash accepted", err)
	}
	cancelled, err := scanner.CancelPlannedJob(ctx, actor, project, first.ID, first.PlanHash)
	if err != nil || cancelled.State != "CANCELLED" || cancelled.PlanHash != first.PlanHash {
		t.Fatal("cancel failed", cancelled, err)
	}
	cancelReplay, err := scanner.CancelPlannedJob(ctx, actor, project, first.ID, first.PlanHash)
	if err != nil || cancelReplay != cancelled {
		t.Fatal("cancel replay changed state", cancelReplay, err)
	}
	if _, err := blobs.ReadObject(ctx, project, object.Hash); err != nil {
		t.Fatal("preview or cancel removed content", err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_receipts").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("preview manufactured execution receipts", err)
	}
	archivePath := filepath.Join(t.TempDir(), "actual-cleanup-plans.zip")
	if _, err := backup.Create(ctx, db, blobs, archivePath); err != nil {
		t.Fatal("real saved previews break backup", err)
	}
	archiveFile, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer archiveFile.Close()
	stat, err := archiveFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	archived, err := backup.ValidateArchive(archiveFile, stat.Size())
	if err != nil || len(archived.Data["retention_jobs"]) != 2 {
		t.Fatal("saved previews did not survive archive admission", err)
	}
	if _, err := db.Exec("UPDATE retention_jobs SET restored_disabled=TRUE WHERE id=?", second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.CancelPlannedJob(ctx, actor, project, second.ID, second.PlanHash); err != domain.ErrConflict {
		t.Fatal("restored disabled job was changed", err)
	}
	if _, err := db.Exec("UPDATE retention_jobs SET plan_hash=? WHERE id=?", strings.Repeat("b", 64), second.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ReadJob(ctx, actor, project, second.ID); err != domain.ErrNotReady {
		t.Fatal("corrupt saved preview accepted", err)
	}
	encoded, err := json.Marshal(first)
	if err != nil || strings.Contains(string(encoded), object.Hash) || strings.Contains(string(encoded), "must remain") || strings.Contains(string(encoded), "request_hash") {
		t.Fatal("preview exposed private material", err)
	}
}

func TestRetentionJobPlanRollbackAndRetry(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "清理预览保存故障", "retention-job-rollback")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
	ctx := context.Background()
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	object, err := blobs.PutObject(ctx, project, strings.NewReader("new orphan during failed plan"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_cleanup_plan BEFORE INSERT ON retention_jobs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='injected plan persistence failure'"); err != nil {
		t.Fatal(err)
	}
	op := "request-v2:" + service.NewID()
	if _, err := scanner.PlanJob(ctx, actor, project, op); err == nil {
		t.Fatal("injected persistence failure accepted")
	}
	var count int
	for _, query := range []string{"SELECT COUNT(*) FROM retention_jobs", "SELECT COUNT(*) FROM retention_objects", "SELECT COUNT(*) FROM project_lifecycle WHERE state='CURRENT'"} {
		if err := db.QueryRow(query).Scan(&count); err != nil || count != 0 {
			t.Fatal("plan/checkpoint/object observation did not roll back", query, count, err)
		}
	}
	if _, err := blobs.ReadObject(ctx, project, object.Hash); err != nil {
		t.Fatal("failed plan touched physical object", err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_cleanup_plan"); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.PlanJob(ctx, actor, project, op); err != nil {
		t.Fatal("retry after rollback failed", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_jobs").Scan(&count); err != nil || count != 1 {
		t.Fatal("wrong job count", count, err)
	}
}

func TestRetentionJobConcurrentRetriesAndOwnerIsolation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "并发清理预览", "retention-job-concurrent")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	ctx := context.Background()
	op := "request-v2:" + service.NewID()
	type result struct {
		report service.RetentionJobReport
		err    error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			report, err := scanner.PlanJob(ctx, actor, project, op)
			results <- result{report, err}
		}()
	}
	start.Done()
	for range 2 {
		r := <-results
		// An interleaved first phase can invalidate the older scan epoch. That
		// request may retry; it must never create a second plan or scan old data.
		if r.err != nil && r.err != domain.ErrNotReady {
			t.Fatal("unexpected concurrent failure", r.err)
		}
	}
	replay, err := scanner.PlanJob(ctx, actor, project, op)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_jobs WHERE project_id=?", project).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate concurrent plans", count, err)
	}
	foreignWorkspace, foreignActor := service.NewID(), service.NewID()
	if _, err := db.Exec("INSERT INTO workspaces(id,name) VALUES(?,'foreign')", foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'foreign-job','unused','owner')", foreignActor, foreignWorkspace); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ReadJob(ctx, foreignActor, project, replay.ID); err != domain.ErrForbidden {
		t.Fatal("foreign owner read job", err)
	}
	if _, err := scanner.PlanJob(ctx, foreignActor, project, op); err != domain.ErrForbidden {
		t.Fatal("foreign owner replayed job", err)
	}
	if _, err := scanner.CancelPlannedJob(ctx, foreignActor, project, replay.ID, replay.PlanHash); err != domain.ErrForbidden {
		t.Fatal("foreign owner cancelled job", err)
	}
	if _, err := scanner.PlanJob(ctx, actor, project, "my secret novel title"); err != domain.ErrValidation {
		t.Fatal("free text operation accepted", err)
	}
	if _, err := scanner.ListJobs(ctx, foreignActor, project, ""); err != domain.ErrForbidden {
		t.Fatal("foreign owner listed jobs", err)
	}
	if _, err := db.Exec("ALTER TABLE retention_jobs MODIFY created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)"); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.ReadJob(ctx, actor, project, replay.ID); err != domain.ErrNotReady {
		t.Fatal("drifted job clock schema accepted", err)
	}
}

func TestRetentionJobHistoryPaginationUsesSavedPlans(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "清理预览历史", "retention-history")
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	ctx := context.Background()
	first, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{first.ID: true}
	for range 50 {
		id, op := service.NewID(), "request-v2:"+service.NewID()
		_, err := db.Exec(`INSERT INTO retention_jobs(id,project_id,actor_id,operation_id,request_hash,inventory_revision,inventory_hash,plan_hash,plan,created_at,updated_at)
			SELECT ?,project_id,actor_id,?,SHA2(CONCAT('retention-plan-v1/',project_id,'/',actor_id,'/',?),256),inventory_revision,inventory_hash,plan_hash,plan,created_at,updated_at FROM retention_jobs WHERE id=?`, id, op, op, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		want[id] = true
	}
	page, err := scanner.ListJobs(ctx, actor, project, "")
	if err != nil || len(page.Items) != 50 || page.NextCursor == "" {
		t.Fatal("first page", len(page.Items), err)
	}
	for _, item := range page.Items {
		if !want[item.ID] {
			t.Fatal("unexpected or repeated item")
		}
		delete(want, item.ID)
	}
	last, err := scanner.ListJobs(ctx, actor, project, page.NextCursor)
	if err != nil || len(last.Items) != 1 || last.NextCursor != "" || !want[last.Items[0].ID] {
		t.Fatal("last page", last, err)
	}
	if _, err := scanner.ListJobs(ctx, actor, project, service.NewID()); err != domain.ErrForbidden {
		t.Fatal("foreign or missing cursor accepted", err)
	}
}
