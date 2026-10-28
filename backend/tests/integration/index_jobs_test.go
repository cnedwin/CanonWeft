package integration

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
	"os"
	"testing"
	"time"
)

func TestBackgroundIndexUsesOutboxAndCannotPublishOrReuseStaleInputs(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	pid := createProject(b, "后台索引", "index-job")
	bid := branch(b, pid, "正史")
	resourceID := resource(b, pid, bid, "长篇资料")
	release := prepareRelease(b, pid, bid, "", "", "prospective-source", []map[string]any{{"resource_id": resourceID, "text": "剑客走进山洞，寻找遗失的长剑。洞穴中流淌着一条地下河。"}})
	releaseAdopt(b, pid, release)
	root := "/api/projects/" + pid + "/branches/" + bid + "/knowledge/jobs"
	input := map[string]any{"operation_id": "background-index", "release_id": release}
	job := b.request("POST", root, input, 202)
	id := job["id"].(string)
	if retry := b.request("POST", root, input, 202); retry["id"] != id {
		t.Fatal("duplicate index job")
	}
	input["kind"] = "published"
	if retry := b.request("POST", root, input, 202); retry["id"] != id {
		t.Fatal("explicit published kind changed historical fingerprint")
	}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: ip}, os.Getenv("STUDIO_TEMPORAL"), "studio-index-integration")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if runtime != nil {
			runtime.Close()
		}
	}()
	ready := false
	for i := 0; i < 100; i++ {
		current := b.request("GET", root+"/"+id, nil, 200)
		if current["state"] == "READY" {
			ready = true
			break
		}
		if current["state"] == "FAILED" || current["state"] == "BLOCKED" {
			t.Fatal(current)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatal("background workflow did not build index")
	}
	if b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)["head"] != "" {
		t.Fatal("index preparation published candidate")
	}
	var delivered int
	if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='index.start' AND resource_id=? AND processed_at IS NOT NULL", id).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatal("outbox not delivered", err)
	}
	// Publishing consumes the already prepared exact manifest even if inference goes offline.
	oldURL := os.Getenv("STUDIO_EMBEDDING_URL")
	t.Setenv("STUDIO_EMBEDDING_URL", "http://127.0.0.1:1")
	releasePublish(b, pid, release)
	t.Setenv("STUDIO_EMBEDDING_URL", oldURL)
	me := b.request("GET", "/api/me", nil, 200)
	a := domain.Actor{ID: me["id"].(string)}
	embedder, err := retrieval.NewEmbedder(oldURL, os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.IndexJobs{Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	// Stop delivery before creating the remaining tasks so cancellation is deterministic.
	runtime.Close()
	runtime = nil
	cancelled, err := svc.Create(ctx, a, pid, bid, service.IndexJobInput{OperationID: "cancel-index"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Cancel(ctx, a, pid, bid, cancelled.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, cancelled.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	stale, err := svc.Create(ctx, a, pid, bid, service.IndexJobInput{OperationID: "stale-index"})
	if err != nil {
		t.Fatal(err)
	}
	next := prepareRelease(b, pid, bid, "", "", "changed-source", []map[string]any{{"resource_id": resourceID, "expected_head": release, "text": "原文已经修订为新的场景。"}})
	releaseAdopt(b, pid, next)
	releasePublish(b, pid, next)
	if state, err := svc.Execute(ctx, stale.ID); err != nil || state != "BLOCKED" {
		t.Fatal("stale task not blocked", state, err)
	}
	before, err := svc.Knowledge.Status(ctx, a, pid, bid)
	if err != nil {
		t.Fatal(err)
	}
	failedRelease := prepareRelease(b, pid, bid, "", "", "failure-source", []map[string]any{{"resource_id": resourceID, "expected_head": next, "text": "不能丢失旧索引的不同新材料。"}})
	releaseAdopt(b, pid, failedRelease)
	failed, err := svc.Create(ctx, a, pid, bid, service.IndexJobInput{OperationID: "offline-index", ReleaseID: failedRelease})
	if err != nil {
		t.Fatal(err)
	}
	unavailable, err := retrieval.NewEmbedder("http://127.0.0.1:1", os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	svc.Knowledge.Embedder = unavailable
	if state, err := svc.Execute(ctx, failed.ID); err != nil || state != "FAILED" {
		t.Fatal(state, err)
	}
	if current, err := svc.Knowledge.Status(ctx, a, pid, bid); err != nil || current.ID != before.ID {
		t.Fatal("failure replaced old index", current, err)
	}
	if b.request("GET", "/api/projects/"+pid+"/resources/"+resourceID, nil, 200)["head"] != next {
		t.Fatal("failure changed publication")
	}
}
