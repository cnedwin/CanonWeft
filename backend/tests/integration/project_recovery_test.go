package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/memory"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestProjectRecoveryAPICommitsCompleteCopyOnceAndRebuildsKnowledge(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	source := createProject(b, "待恢复小说", "recover-api-source")
	branchID := branch(b, source, "正史")
	head := propose(b, source, branchID, "", "recover-api-world", originalSnapshot())
	publish(b, source, branchID, head)
	resourceID := resource(b, source, branchID, "正文")
	body := "林舟追查失踪案，保持守护亲人的主线。"
	release := prepareRelease(b, source, branchID, head, "", "recover-api-body", []map[string]any{{"resource_id": resourceID, "text": body}})
	releaseAdopt(b, source, release)
	releasePublish(b, source, release)
	hash := b.request("GET", "/api/projects/"+source+"/resources/"+resourceID, nil, 200)["hash"].(string)
	memoryRoot := "/api/projects/" + source + "/branches/" + branchID + "/memory"
	session := b.request("POST", memoryRoot+"/sessions", service.MemorySessionInput{OperationID: "recover-api-session", Document: service.MemorySessionDocument{Title: "写作上下文"}}, 200)
	item := decodeMemory(t, b.request("POST", memoryRoot+"/items", service.MemoryInput{OperationID: "recover-api-memory", Item: memory.Item{Session: session["id"].(string), Kind: "mainline", Visibility: "author", Control: true, Pinned: true, Text: "守护亲人", Sources: []memory.Source{{Resource: resourceID, Revision: release, Hash: hash, Start: 0, End: len([]rune(body))}}}}, 200))
	b.request("POST", memoryRoot+"/items/"+item.ID+"/adopt", service.MemoryAction{ExpectedRevision: item.Revision, OperationID: "recover-api-adopt-memory"}, 200)
	input := service.ProjectRecoveryInput{Name: "恢复后的小说", Confirmation: "待恢复小说", Revision: 1, OperationID: "recover-api-once"}
	path := "/api/projects/" + source + "/recover"
	b.request("POST", path, input, 409)
	b.request("DELETE", "/api/projects/"+source, map[string]any{"confirm_name": "待恢复小说", "revision": 1, "operation_id": "delete-recover-api-source"}, 200)
	input.Revision = 2
	bad := input
	bad.Confirmation = "错误名称"
	b.request("POST", path, bad, 400)
	bad = input
	bad.Revision = 1
	b.request("POST", path, bad, 409)
	b.request("POST", "/api/members", map[string]any{"username": "recover-reader", "password": "recover-reader-password", "role": "reader"}, 201)
	reader := &browser{t: t, handler: b.handler}
	reader.request("POST", "/api/login", map[string]any{"username": "recover-reader", "password": "recover-reader-password"}, 200)
	reader.request("POST", path, input, 403)
	if _, err := db.Exec("CREATE TRIGGER project_recovery_fault BEFORE INSERT ON index_jobs FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test late project copy fault'"); err != nil {
		t.Fatal(err)
	}
	b.request("POST", path, input, 500)
	if _, err := db.Exec("DROP TRIGGER project_recovery_fault"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 1 {
		t.Fatal("partial project became visible", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM operations WHERE kind='project.recover'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed copy left a success receipt", count, err)
	}
	entries, err := os.ReadDir(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == source {
			continue
		}
		files, err := os.ReadDir(filepath.Join(os.Getenv("STUDIO_BLOB_ROOT"), entry.Name()))
		if err != nil || len(files) != 0 {
			t.Fatal("rolled-back recovery left physical author material", entry.Name(), len(files), err)
		}
	}
	restored := b.request("POST", path, input, 201)
	target := restored["id"].(string)
	if target == source {
		t.Fatal("recovery reused original identity")
	}
	if retry := b.request("POST", path, input, 201); retry["id"] != target {
		t.Fatal("retry duplicated copy", retry)
	}
	bad = input
	bad.Name = "不同副本"
	b.request("POST", path, bad, 409)
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 2 {
		t.Fatal("duplicate recovered projects", count, err)
	}
	var copiedBranch, copiedResource, copiedHead string
	if err := db.QueryRow("SELECT id,head_revision FROM branches WHERE project_id=?", target).Scan(&copiedBranch, &copiedHead); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT id FROM resources WHERE project_id=?", target).Scan(&copiedResource); err != nil {
		t.Fatal(err)
	}
	if copiedBranch == branchID || copiedHead == head || copiedResource == resourceID {
		t.Fatal("copy retained original content identity")
	}
	loaded := b.request("GET", "/api/projects/"+target+"/resources/"+copiedResource, nil, 200)
	if loaded["text"] != body {
		t.Fatal("body missing", loaded)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE project_id=?", target).Scan(&count); err != nil || count != 2 {
		t.Fatal("memory history missing", count, err)
	}
	rows, err := db.Query("SELECT id FROM index_jobs WHERE project_id=?", target)
	if err != nil {
		t.Fatal(err)
	}
	jobs := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		jobs = append(jobs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil || len(jobs) == 0 {
		t.Fatal("index rebuild not queued", err)
	}
	embedder, err := retrieval.NewEmbedder(os.Getenv("STUDIO_EMBEDDING_URL"), os.Getenv("STUDIO_EMBEDDING_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	indexer := &service.IndexJobs{Knowledge: &service.Knowledge{IP: ip, Embedder: embedder}}
	for _, id := range jobs {
		if state, e := indexer.Execute(ctx, id); e != nil || state != "READY" {
			t.Fatal("recovered knowledge cannot rebuild", state, e)
		}
	}
	if _, err = backup.Create(ctx, db, ip.Blobs, filepath.Join(t.TempDir(), "recovered-project.zip")); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+target, map[string]any{"confirm_name": "恢复后的小说", "revision": 1, "operation_id": "delete-restored-copy"}, 200)
	b.request("POST", path, input, 403)
	var originalHead string
	if err = db.QueryRow("SELECT head_revision FROM resources WHERE id=?", resourceID).Scan(&originalHead); err != nil || originalHead != release {
		t.Fatal("copy deletion changed original history", originalHead, err)
	}
}

func TestProjectRecoveryRejectsExpiredMaterialWithoutPartialCopy(t *testing.T) {
	b, db := setup(t)
	source := createProject(b, "有过期材料", "recover-expired-source")
	root := "/api/projects/" + source + "/skills"
	pkg := b.request("POST", root, map[string]any{"operation_id": "expired-method", "markdown": "---\nname: expired-method\ndescription: Source review.\nlicense: LicenseRef-Private\n---\n保留来源。"}, 201)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	expireTransientRecord(t, db, actor, source, "skill_packages", "report", pkg["id"].(string))
	// The expiry fixture installs pending schema 36. Exercise its service
	// transaction directly; the schema-35 HTTP guard must remain closed.
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	if err := ip.Workspace.DeleteProject(context.Background(), actor, source, "有过期材料", 1, "delete-expired-recovery"); err != nil {
		t.Fatal(err)
	}
	if _, err := ip.RecoverProject(context.Background(), actor, source, service.ProjectRecoveryInput{Name: "不可完整复制", Confirmation: "有过期材料", Revision: 2, OperationID: "recover-expired"}); !errors.Is(err, service.ErrRecoveryIncomplete) {
		t.Fatal("expired material was not reported", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 1 {
		t.Fatal("expired material produced a partial copy", count, err)
	}
}

func TestRecoveredProjectSupportsRetentionScanAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	source := createProject(b, "恢复后的保留登记", "recovery-retention-source")
	branchID := branch(b, source, "正史")
	head := propose(b, source, branchID, "", "recovery-retention-world", originalSnapshot())
	publish(b, source, branchID, head)
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	installCompleteRetentionFixture(t, db)
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: ip.Blobs.(storage.LocalBlobs)}
	if _, err := scanner.Scan(ctx, actor, source); err != nil {
		t.Fatal("source scan", err)
	}
	if err := ip.Workspace.DeleteProject(ctx, actor, source, "恢复后的保留登记", 1, "delete-recovery-retention"); err != nil {
		t.Fatal(err)
	}
	input := service.ProjectRecoveryInput{Name: "保留登记副本", Confirmation: "恢复后的保留登记", Revision: 2, OperationID: "recover-retention-once"}
	copy, err := ip.RecoverProject(ctx, actor, source, input)
	if err != nil {
		t.Fatal("recovery with installed retention", err)
	}
	report, err := scanner.Scan(ctx, actor, copy.ID)
	if err != nil || !report.Complete || report.State != "CURRENT" || report.References == 0 {
		t.Fatal("copy scan", report, err)
	}
	if retry, err := ip.RecoverProject(ctx, actor, source, input); err != nil || retry.ID != copy.ID {
		t.Fatal("registered recovery receipt cannot be replayed", retry, err)
	}
	var recoveryState string
	if err := db.QueryRow("SELECT state FROM retention_recovery_attempts WHERE target_project_id=? AND project_id=?", copy.ID, source).Scan(&recoveryState); err != nil || recoveryState != "COMMITTED" {
		t.Fatal("recovery lacks a durable commit decision", recoveryState, err)
	}
	var recordedObjects int
	if err := db.QueryRow("SELECT JSON_LENGTH(objects) FROM retention_recovery_attempts WHERE target_project_id=?", copy.ID).Scan(&recordedObjects); err != nil || recordedObjects == 0 {
		t.Fatal("committed recovery did not journal its actual writes", recordedObjects, err)
	}
	if _, err := backup.Create(ctx, db, ip.Blobs, filepath.Join(t.TempDir(), "recovered-with-retention.zip")); err != nil {
		t.Fatal("copy archive", err)
	}
}
