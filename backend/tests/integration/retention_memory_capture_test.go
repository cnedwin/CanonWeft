package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/service"
)

func TestRetentionMemorySaveAdoptEditAndRevokeCapture(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "记忆保存来源", "memory-capture")
	bid := branch(b, project, "正史")
	rid := resource(b, project, bid, "主题来源")
	release := prepareRelease(b, project, bid, "", "", "memory-source", []map[string]any{{"resource_id": rid, "text": "守护亲人是故事的主题。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	source := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip, ctx := propagationService(t, db), context.Background()
	svc := &service.Memory{IP: ip}
	session, err := svc.SaveSession(ctx, actor, project, bid, service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "本次创作"}})
	if err != nil {
		t.Fatal(err)
	}
	in := service.MemoryInput{OperationID: "memory-save", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Control: true, Text: "守护亲人", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}}
	legacyInput := in
	legacyInput.OperationID = "legacy-save"
	legacy, err := svc.Save(ctx, actor, project, bid, legacyInput)
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	item, err := svc.Save(ctx, actor, project, bid, in)
	if err != nil {
		t.Fatal(err)
	}
	initial := capturedVersion(t, db, project, "memory_items", item.ID, "document")
	firstRevision := capturedVersion(t, db, project, "memory_revisions", item.Revision, "document")
	if initial.Reference.ContentRoot || firstRevision.Reference.ContentRoot {
		t.Fatal("private candidate was adopted by source capture")
	}
	// Recording proof and publishing the memory must be one transaction.
	if _, err := db.Exec("CREATE TRIGGER reject_memory_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='memory_revisions.document' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected memory proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_memory_proof") })
	action := service.MemoryAction{OperationID: "memory-adopt", ExpectedRevision: item.Revision}
	if _, err := svc.Transition(ctx, actor, project, bid, item.ID, "adopt", action); err == nil {
		t.Fatal("adoption committed without immutable proof")
	}
	var head, state string
	var count int
	if err := db.QueryRow("SELECT head_revision,state FROM memory_items WHERE id=?", item.ID).Scan(&head, &state); err != nil || head != item.Revision || state != "candidate" {
		t.Fatal("failed proof changed memory authority", head, state, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE item_id=?", item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed adoption left a revision", count, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_memory_proof"); err != nil {
		t.Fatal(err)
	}
	adopted, err := svc.Transition(ctx, actor, project, bid, item.ID, "adopt", action)
	if err != nil {
		t.Fatal(err)
	}
	proof := capturedVersion(t, db, project, "memory_revisions", adopted.Revision, "document")
	if !proof.Reference.ContentRoot {
		t.Fatal("adopted immutable memory lacks preservation root")
	}
	in.Item, in.ExpectedRevision, in.OperationID = adopted, adopted.Revision, "memory-edit"
	in.Item.Session, in.Item.Text = session.ID, "主角学会守护亲人"
	edited, err := svc.Save(ctx, actor, project, bid, in)
	if err != nil {
		t.Fatal(err)
	}
	if capturedVersion(t, db, project, "memory_revisions", edited.Revision, "document").Reference.ContentRoot {
		t.Fatal("editing adopted memory promoted a new candidate")
	}
	if capturedVersion(t, db, project, "memory_items", edited.ID, "document").Reference.ContentRoot {
		t.Fatal("changed current memory inherited the old adopted version's retention")
	}
	// Revocation changes authority, not the source body. A temporarily
	// unavailable blob store must not prevent the author from revoking it.
	blobs := ip.Blobs
	ip.Blobs = nil
	if _, err := svc.Transition(ctx, actor, project, bid, item.ID, "revoke", service.MemoryAction{OperationID: "memory-revoke", ExpectedRevision: edited.Revision}); err != nil {
		t.Fatal(err)
	}
	ip.Blobs = blobs
	current := capturedVersion(t, db, project, "memory_items", item.ID, "document")
	if current.Reference.Revision != initial.Reference.Revision+3 || !current.Reference.CreatedAt.Equal(initial.Reference.CreatedAt) {
		t.Fatal("memory history or original observation age changed", current, initial)
	}
	old := capturedVersion(t, db, project, "memory_revisions", adopted.Revision, "document")
	if old.Reference.Revision != proof.Reference.Revision || !old.Reference.ContentRoot {
		t.Fatal("revoke altered the historical adopted version")
	}
	legacyInput.Item, legacyInput.ExpectedRevision, legacyInput.OperationID = legacy, legacy.Revision, "legacy-edit"
	legacyInput.Item.Text = "旧记忆首次编辑"
	if _, err := svc.Save(ctx, actor, project, bid, legacyInput); err != nil {
		t.Fatal(err)
	}
	capturedVersion(t, db, project, "memory_revisions", legacy.Revision, "document")
	if capturedVersion(t, db, project, "memory_items", legacy.ID, "document").Reference.Revision != 2 {
		t.Fatal("first edit lost legacy current document")
	}
	if _, err := ip.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil {
		t.Fatal("inventory disagrees with memory writer", err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='memory_revisions' AND field_key='document'", project).Scan(&count); err != nil || count != 6 {
		t.Fatal("immutable memory proofs missing", count, err)
	}
}
