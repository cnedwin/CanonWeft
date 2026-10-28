package integration

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
)

func TestRetentionConfirmedJobRunsAfterRestart(t *testing.T) {
	t.Setenv("STUDIO_RETENTION_EXECUTION_ENABLED", "")
	b, db := setup(t)
	project := createProject(b, "后台清理完整流程", "cleanup-flow")
	member := b.request("POST", "/api/members", map[string]any{"username": "reader", "password": "reader-password-123", "role": "reader"}, 201)
	b.request("PUT", "/api/projects/"+project+"/members/"+member["id"].(string), map[string]any{"role": "reader"}, 200)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "reader", "password": "reader-password-123"}, 200)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	if enabled := b.request("GET", "/api/operations/retention-execution", nil, 200)["enabled"]; enabled != false {
		t.Fatal("unexpected automatic activation")
	}
	b.request("POST", "/api/projects/"+project+"/retention-jobs/"+strings.Repeat("1", 32)+"/start", map[string]any{"plan_hash": strings.Repeat("a", 64)}, 503)
	installCompleteRetentionFixture(t, db)
	files, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer files.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	object, err := files.PutObject(ctx, project, strings.NewReader("expired unused cleanup flow object"))
	if err != nil {
		t.Fatal(err)
	}
	scanner := &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: files}
	if _, err := scanner.Scan(ctx, actor, project); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE retention_objects SET first_seen_at='2020-01-01' WHERE project_id=? AND object_hash=?", project, object.Hash); err != nil {
		t.Fatal(err)
	}
	preview, err := scanner.PlanJob(ctx, actor, project, "request-v2:"+service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	id, hash := preview.ID, preview.PlanHash
	path := "/api/projects/" + project + "/retention-jobs/" + id
	b.request("POST", path+"/start", map[string]any{"plan_hash": hash}, 503)
	t.Setenv("STUDIO_RETENTION_EXECUTION_ENABLED", "1")
	// The installed production migration now admits explicit HTTP confirmation.
	b.request("POST", path+"/start", map[string]any{"plan_hash": hash}, 202)
	if _, err := scanner.StartJob(ctx, member["id"].(string), project, id, hash); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader started cleanup", err)
	}
	if _, err := scanner.StartJob(ctx, actor, project, id, strings.Repeat("0", 64)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong confirmation accepted", err)
	}
	for range 2 {
		if _, err := scanner.StartJob(ctx, actor, project, id, hash); err != nil {
			t.Fatal(err)
		}
	}
	var attempts int
	if err := db.QueryRow("SELECT attempt FROM retention_jobs WHERE id=?", id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("replayed confirmation duplicated start", attempts, err)
	}
	// Recreate service composition before starting its previously absent worker.
	// The confirmed task must survive without any in-memory delivery callback.
	scanner = &service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: files}
	if job, err := scanner.ReadJob(ctx, actor, project, id); err != nil || job.State != "DETACHING" {
		t.Fatal("restart lost queued task", job.State, err)
	}
	ip := propagationService(t, db)
	runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "cleanup-"+service.NewID())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	for {
		var state string
		if err := db.QueryRow("SELECT state FROM retention_jobs WHERE id=?", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "COMPLETE" {
			break
		}
		if state == "BLOCKED" || ctx.Err() != nil {
			t.Fatal("background cleanup did not complete", state, ctx.Err())
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, err := files.ReadObject(ctx, project, object.Hash); err == nil {
		t.Fatal("workflow completed without deleting file")
	}
	if job, err := scanner.ReadJob(ctx, actor, project, id); err != nil || job.State != "COMPLETE" {
		t.Fatal("completed result unavailable", job.State, err)
	}
	if err := scanner.HoldUnavailableCleanup(ctx, id); err != nil {
		t.Fatal(err)
	}
	if job, err := scanner.ReadJob(ctx, actor, project, id); err != nil || job.State != "COMPLETE" {
		t.Fatal("late failure overwrote completed job", job.State, err)
	}
	if _, err := scanner.StartJob(ctx, actor, project, id, hash); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT attempt FROM retention_jobs WHERE id=?", id).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("complete job restarted", attempts, err)
	}
}
