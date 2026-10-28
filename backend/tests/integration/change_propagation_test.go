package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"testing"
	"time"

	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	platformworker "novelstudio/internal/worker"
)

func propagationService(t *testing.T, db *sql.DB) *service.IP {
	t.Helper()
	blobs := storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}
	return &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: blobs}
}

func publicationEvent(t *testing.T, db *sql.DB, source string) string {
	t.Helper()
	var id string
	if err := db.QueryRow("SELECT id FROM outbox WHERE resource_id=? AND kind IN ('ip.published','release.published')", source).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublicationPropagationReplaysHistoricalSourceWithoutChangingNewerText(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "自动变更传播", "propagation-project")
	bid := branch(b, pid, "正史")
	a, c := resource(b, pid, bid, "时间资料"), resource(b, pid, bid, "冻结故事")
	base := prepareRelease(b, pid, bid, "", "", "propagation-base", []map[string]any{{"resource_id": a, "text": "月圆有效"}})
	releaseAdopt(b, pid, base)
	releasePublish(b, pid, base)
	source := dependencyFromCatalog(t, b, pid, "resource", a)
	dependent := prepareRelease(b, pid, bid, "", "", "propagation-dependent", []map[string]any{{"resource_id": c, "text": "月圆夜的故事", "dependencies": map[string]any{"frozen": true, "inputs": []any{map[string]any{"source": source, "kind": "evidence", "reason": "药效影响故事"}}}}})
	releaseAdopt(b, pid, dependent)
	releasePublish(b, pid, dependent)
	changed := prepareRelease(b, pid, bid, "", "", "propagation-change", []map[string]any{{"resource_id": a, "expected_head": base, "text": "新月有效"}})
	releaseAdopt(b, pid, changed)
	releasePublish(b, pid, changed)
	later := prepareRelease(b, pid, bid, "", "", "propagation-later", []map[string]any{{"resource_id": a, "expected_head": changed, "text": "雨夜有效"}})
	releaseAdopt(b, pid, later)
	releasePublish(b, pid, later)
	event := publicationEvent(t, db, changed)
	// Outbox free-form content is transport data, never an authoritative source.
	if _, err := db.Exec("UPDATE outbox SET payload=JSON_OBJECT('text','伪造事件正文') WHERE id=?", event); err != nil {
		t.Fatal(err)
	}
	svc := propagationService(t, db)
	result, err := svc.PropagatePublication(context.Background(), event)
	if err != nil || result.State != "COMPLETED" || result.PlanID == "" {
		t.Fatal(result, err)
	}
	replayed, err := svc.PropagatePublication(context.Background(), event)
	if err != nil || replayed != result {
		t.Fatal("non-idempotent propagation", replayed, err)
	}
	root := "/api/projects/" + pid
	plan := b.request("GET", root+"/changes/"+result.PlanID, nil, 200)
	if plan["source_event_id"] != event || plan["source_state"] != "SUPERSEDED" || plan["stale"] != true || plan["semantic_review"] != "NOT_RUN" {
		t.Fatal(plan)
	}
	items := plan["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["action"] != "DECIDE" {
		t.Fatal("frozen impact missing", plan)
	}
	diff := plan["differences"].([]any)[0].(map[string]any)["fields"].([]any)[0].(map[string]any)
	if diff["before"] != "月圆有效" || diff["after"] != "新月有效" {
		t.Fatal("read current content instead of historical source", diff)
	}
	for id, expected := range map[string]string{a: "雨夜有效", c: "月圆夜的故事"} {
		if b.request("GET", root+"/resources/"+id, nil, 200)["text"] != expected {
			t.Fatal("propagation rewrote author content")
		}
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM change_plans WHERE project_id=?", pid).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	// A corrupted receipt cannot be reused as an apparently completed review.
	verifyPlanSourceLineage(t, b, db, pid, result.PlanID)
	if _, err = db.Exec("UPDATE change_plans SET content_hash=REPEAT('0',64) WHERE id=?", result.PlanID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.PropagatePublication(context.Background(), event); err == nil {
		t.Fatal("corrupted receipt accepted")
	}
}

func TestPublicationPropagationIPEventCrossBranchAndDeletedProject(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "自动设定检查", "propagation-ip")
	canon, adapted := branch(b, pid, "正史"), branch(b, pid, "改编")
	base := propose(b, pid, canon, "", "propagation-ip-base", originalSnapshot())
	publish(b, pid, canon, base)
	resourceID := resource(b, pid, adapted, "人物视觉")
	source := dependencyFromCatalog(t, b, pid, "entity", "lin-1")
	release := prepareRelease(b, pid, adapted, "", "", "propagation-visual", []map[string]any{{"resource_id": resourceID, "text": "旧的人物形象", "dependencies": map[string]any{"inputs": []any{map[string]any{"source": source, "kind": "representation", "reason": "角色视觉依赖正史"}}}}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	next := originalSnapshot()
	next.Entities[0].Description = "短发，左眼有伤痕"
	changed := propose(b, pid, canon, base, "propagation-ip-next", next)
	publish(b, pid, canon, changed)
	event := publicationEvent(t, db, changed)
	svc := propagationService(t, db)
	result, err := svc.PropagatePublication(context.Background(), event)
	if err != nil || result.State != "COMPLETED" {
		t.Fatal(result, err)
	}
	var raw []byte
	if err = db.QueryRow("SELECT payload FROM change_plans WHERE id=?", result.PlanID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plan service.ChangePlan
	if err = json.Unmarshal(raw, &plan); err != nil || len(plan.Items) != 1 || plan.Items[0].Node.ID != resourceID || plan.SourceState != "CURRENT" {
		t.Fatal(plan, err)
	}
	// A deleted project does not allow redelivery to recreate private content.
	b.request("DELETE", "/api/projects/"+pid, map[string]any{"confirm_name": "自动设定检查", "revision": 1, "operation_id": "delete-propagation"}, 200)
	result, err = svc.PropagatePublication(context.Background(), event)
	if err != nil || result.State != "CANCELLED" || result.PlanID != "" {
		t.Fatal(result, err)
	}
	result, err = svc.PropagatePublication(context.Background(), publicationEvent(t, db, base))
	if err != nil || result.State != "CANCELLED" || result.PlanID != "" {
		t.Fatal("unprocessed deleted-project event was recreated", result, err)
	}
	verifyPlanSourceLineage(t, b, db, pid, plan.ID)
}

func TestPublicationPropagationRuntimeRestartRedeliversAcknowledgedEventOnce(t *testing.T) {
	b, db := setup(t)
	pid := createProject(b, "持久传播恢复", "propagation-runtime")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "版本资料")
	release := prepareRelease(b, pid, bid, "", "", "propagation-runtime-release", []map[string]any{{"resource_id": rid, "text": "保持不变的作者正文"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	event := publicationEvent(t, db, release)
	svc := propagationService(t, db)
	namespace := "studio-propagation-" + service.NewID()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	start := func() *platformworker.Runtime {
		runtime, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: svc}, os.Getenv("STUDIO_TEMPORAL"), namespace)
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	wait := func() string {
		deadline := time.NewTimer(30 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			var count, acknowledged int
			var id sql.NullString
			err := db.QueryRow("SELECT COUNT(*),MIN(id) FROM change_plans WHERE project_id=?", pid).Scan(&count, &id)
			if err != nil {
				t.Fatal(err)
			}
			if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE id=? AND processed_at IS NOT NULL", event).Scan(&acknowledged); err != nil {
				t.Fatal(err)
			}
			if count == 1 && acknowledged == 1 {
				return id.String
			}
			select {
			case <-deadline.C:
				t.Fatal("persistent propagation did not complete", count, acknowledged)
			case <-tick.C:
			}
		}
	}
	first := start()
	var originalID string
	func() { defer first.Close(); originalID = wait() }()
	// Simulate losing the delivery acknowledgement after the business commit.
	if _, err := db.Exec("UPDATE outbox SET processed_at=NULL WHERE id=?", event); err != nil {
		t.Fatal(err)
	}
	second := start()
	defer second.Close()
	if recovered := wait(); recovered != originalID {
		t.Fatal("restart duplicated completed impact", originalID, recovered)
	}
	current := b.request("GET", "/api/projects/"+pid+"/resources/"+rid, nil, 200)
	if current["head"] != release || current["text"] != "保持不变的作者正文" {
		t.Fatal("worker overwrote source", current)
	}
}

func TestPublicationPropagationLocksAttributionBeforeProject(t *testing.T) {
	b, db := setup(t)
	observer, schema := publicationLockObserver(t, db)
	pid := createProject(b, "并发传播", "propagation-lock-order")
	bid := branch(b, pid, "正史")
	rid := resource(b, pid, bid, "正文")
	release := prepareRelease(b, pid, bid, "", "", "propagation-lock-release", []map[string]any{{"resource_id": rid, "text": "作者原稿"}})
	releaseAdopt(b, pid, release)
	releasePublish(b, pid, release)
	event := publicationEvent(t, db, release)
	writer, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Rollback()
	var actorID string
	if err = writer.QueryRow("SELECT id FROM users WHERE username='owner' FOR UPDATE").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	finished := make(chan error, 1)
	svc := propagationService(t, db)
	go func() { _, e := svc.PropagatePublication(ctx, event); finished <- e }()
	// Wait for the real FK/author-row contention, then inspect project ownership.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting int
		err = observer.QueryRowContext(ctx, `SELECT COUNT(*) FROM performance_schema.data_lock_waits w JOIN performance_schema.data_locks l ON l.ENGINE_LOCK_ID=w.REQUESTING_ENGINE_LOCK_ID WHERE l.OBJECT_SCHEMA=? AND l.OBJECT_NAME='users'`, schema).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("propagation did not reach attribution lock", ctx.Err())
		case <-tick.C:
		}
	}
	probe, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var locked string
	lockErr := probe.QueryRow("SELECT id FROM projects WHERE id=? FOR UPDATE NOWAIT", pid).Scan(&locked)
	probe.Rollback()
	writer.Rollback()
	if err = <-finished; err != nil {
		t.Fatal(err)
	}
	if lockErr != nil {
		t.Fatal("propagation holds project while waiting for the author's row; ordinary author requests take the opposite order", lockErr)
	}
}
