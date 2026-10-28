package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredMemorySessionKeepsAdoptedMemoryAndCannotResume(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "会话过期与长期记忆", "session-expiry")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "主题来源")
	release := prepareRelease(b, project, branchID, "", "", "session-memory-source", []map[string]any{{"resource_id": rid, "text": "守护亲人是故事的主题。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	source := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	svc := &service.Memory{IP: ip}
	in := service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "PRIVATE_CANARY", Notes: "这段临时推理应被清除"}}
	session, err := svc.SaveSession(ctx, actor, project, branchID, in)
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Save(ctx, actor, project, branchID, service.MemoryInput{OperationID: "memory", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Control: true, Text: "守护亲人", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	item, err = svc.Transition(ctx, actor, project, branchID, item.ID, "adopt", service.MemoryAction{OperationID: "adopt", ExpectedRevision: item.Revision})
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	var raw string
	if err := db.QueryRow("SELECT document FROM memory_sessions WHERE id=?", session.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	owner, _ := retention.RowOwnerKey("memory_sessions", map[string]string{"id": session.ID})
	ref, _ := retention.ReferenceID(project, "memory_sessions", owner, "document")
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'memory_sessions',?,'document','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
	job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 1 || job.RemovableObjects != 0 {
		t.Fatal("session-only plan", job, err)
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	state := ""
	for n := 0; n < 6 && state != "COMPLETE"; n++ {
		state, err = scanner.AdvanceJob(ctx, job.ID)
		if err != nil {
			t.Fatal("session cleanup", state, err)
		}
	}
	if state != "COMPLETE" {
		t.Fatal(state)
	}
	if _, err := svc.ReadSession(ctx, actor, project, branchID, session.ID); err != domain.ErrMaterialExpired {
		t.Fatal("expired session readable", err)
	}
	if _, err := svc.Compile(ctx, actor, project, branchID, service.MemoryCompileInput{Purpose: "author", Session: session.ID, Budget: 65536}); err != domain.ErrMaterialExpired {
		t.Fatal("expired session reentered a model context", err)
	}
	if _, err := svc.SaveSession(ctx, actor, project, branchID, in); err != domain.ErrMaterialExpired {
		t.Fatal("old create request resumed session", err)
	}
	edit := in
	edit.ID, edit.ExpectedRevision, edit.OperationID = session.ID, session.Revision, "edit-expired"
	if _, err := svc.SaveSession(ctx, actor, project, branchID, edit); err != domain.ErrMaterialExpired {
		t.Fatal("erased session editable", err)
	}
	listed, err := svc.List(ctx, actor, project, branchID)
	if err != nil || len(listed.Sessions) != 1 || listed.Sessions[0].Retention == nil || listed.Sessions[0].Retention.State != "EXPIRED" || listed.Sessions[0].Document.Notes != "" {
		t.Fatal("verified history", listed, err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != item.ID || listed.Items[0].Text != "守护亲人" || listed.Items[0].State != "adopted_plan" {
		t.Fatal("adopted memory lost", listed.Items)
	}
	in.OperationID, in.Document.Notes = "new-session", "继续创作"
	fresh, err := svc.SaveSession(ctx, actor, project, branchID, in)
	if err != nil || fresh.ID == session.ID {
		t.Fatal("new session", fresh, err)
	}
	pack, err := svc.Compile(ctx, actor, project, branchID, service.MemoryCompileInput{Purpose: "author", Session: fresh.ID, Budget: 65536, RequiredIDs: []string{item.ID}})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.ID != item.ID {
		t.Fatal("new session cannot use adopted long-term memory", pack, err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "memory-session.zip")); err != nil {
		t.Fatal("actual archive", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE reference_id=?", ref); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx, actor, project, branchID); err == nil {
		t.Fatal("missing evidence accepted as history")
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-session-proof.zip")); err == nil {
		t.Fatal("missing session proof archived")
	}
}
