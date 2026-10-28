package integration

import (
	"context"
	"errors"
	"os"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryMemoryTransactionPreservesHistoryAndRollsBackPartialWrites(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	actor := b.request("GET", "/api/me", nil, 200)["id"].(string)
	body := "林舟追查雾城失踪案，最终选择守护亲人。"
	type book struct{ id, branch, resource, release, hash string }
	makeBook := func(name string) book {
		p := createProject(b, name, "create-"+name)
		branchID := branch(b, p, "正史")
		ip := propose(b, p, branchID, "", "ip-"+name, originalSnapshot())
		publish(b, p, branchID, ip)
		resourceID := resource(b, p, branchID, "正文-"+name)
		r := prepareRelease(b, p, branchID, ip, "", "release-"+name, []map[string]any{{"resource_id": resourceID, "text": body}})
		releaseAdopt(b, p, r)
		releasePublish(b, p, r)
		hash := b.request("GET", "/api/projects/"+p+"/resources/"+resourceID, nil, 200)["hash"].(string)
		return book{p, branchID, resourceID, r, hash}
	}
	original, target := makeBook("原作"), makeBook("恢复副本")
	root := "/api/projects/" + original.id + "/branches/" + original.branch + "/memory"
	session := b.request("POST", root+"/sessions", service.MemorySessionInput{OperationID: "recovery-session", Document: service.MemorySessionDocument{Title: "未写完的规划", Notes: "保留作者的意见"}}, 200)
	sid := session["id"].(string)
	input := service.MemoryInput{OperationID: "recovery-mainline", Item: memory.Item{Session: sid, Kind: "mainline", Visibility: "author", Control: true, Pinned: true, Text: "保持守护亲人的主线", Sources: []memory.Source{{Resource: original.resource, Revision: original.release, Hash: original.hash, Start: 0, End: len([]rune(body))}}}}
	first := decodeMemory(t, b.request("POST", root+"/items", input, 200))
	adopted := decodeMemory(t, b.request("POST", root+"/items/"+first.ID+"/adopt", service.MemoryAction{ExpectedRevision: first.Revision, OperationID: "recovery-adopt"}, 200))
	input.OperationID, input.Item.Text = "recovery-candidate", "未采用的下一章想法"
	draft := decodeMemory(t, b.request("POST", root+"/items", input, 200))
	b.request("POST", root+"/sessions", service.MemorySessionInput{ID: sid, ExpectedRevision: 1, OperationID: "recovery-focus", Document: service.MemorySessionDocument{Title: "未写完的规划", Notes: "保留作者的意见", FocusIDs: []string{first.ID, draft.ID}}}, 200)
	ids := memory.RecoveryMap{SourceProject: original.id, TargetProject: target.id, Branches: map[string]string{original.branch: target.branch}, Sessions: map[string]string{sid: service.NewID()}, Items: map[string]string{first.ID: service.NewID(), draft.ID: service.NewID()}, Revisions: map[string]string{first.Revision: service.NewID(), adopted.Revision: service.NewID(), draft.Revision: service.NewID()}, Resources: map[memory.RecoverySource]memory.RecoverySource{{Resource: original.resource, Revision: original.release, Hash: original.hash}: {Resource: target.resource, Revision: target.release, Hash: target.hash}}}
	svc := &service.Memory{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}}
	// A live project is not a recycle-bin source.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CopyRecoveryMemoryTx(ctx, tx, actor, ids)
	tx.Rollback()
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("live project treated as deleted", err)
	}
	var revision int64
	if err = db.QueryRow("SELECT revision FROM projects WHERE id=?", original.id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+original.id, map[string]any{"confirm_name": "原作", "revision": revision, "operation_id": "delete-recovery-original"}, 200)
	reader := b.request("POST", "/api/members", map[string]any{"username": "recovery-reader", "password": "reader-password-123", "role": "reader"}, 201)["id"].(string)
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CopyRecoveryMemoryTx(ctx, tx, reader, ids)
	tx.Rollback()
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("reader gained recovery authority", err)
	}
	// A late insert failure occurs after sessions and heads were written. Even a
	// caller that commits the outer transaction cannot expose a partial copy.
	if _, err = db.Exec("CREATE TRIGGER recovery_memory_fault BEFORE INSERT ON memory_revisions FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test recovery fault'"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, copyErr := svc.CopyRecoveryMemoryTx(ctx, tx, actor, ids)
	if copyErr == nil || counts != (service.RecoveryMemoryCounts{}) {
		tx.Rollback()
		t.Fatal("injected failure did not reject copy", counts, copyErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TRIGGER recovery_memory_fault"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"memory_sessions", "memory_items", "memory_revisions"} {
		var n int
		if err = db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id=?", target.id).Scan(&n); err != nil || n != 0 {
			t.Fatal("partial recovered data visible", table, n, err)
		}
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = svc.CopyRecoveryMemoryTx(ctx, tx, actor, ids)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if counts != (service.RecoveryMemoryCounts{Sessions: 1, Items: 2, Revisions: 3}) {
		tx.Rollback()
		t.Fatal("history lost", counts)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	targetRoot := "/api/projects/" + target.id + "/branches/" + target.branch + "/memory"
	checkpoint := b.request("GET", targetRoot+"/sessions/"+ids.Sessions[sid], nil, 200)
	if checkpoint["revision"] != float64(2) || checkpoint["document"].(map[string]any)["notes"] != "保留作者的意见" {
		t.Fatal("checkpoint lost", checkpoint)
	}
	compiled := b.request("POST", targetRoot+"/compile", service.MemoryCompileInput{Purpose: "writer", Perspective: "lin-1", Timeline: "main", Query: "主线", Budget: 65536}, 200)
	if len(compiled["items"].([]any)) != 1 {
		t.Fatal("restored mainline missing or private candidate published", compiled)
	}
	var parent string
	if err = db.QueryRow("SELECT parent_id FROM memory_revisions WHERE id=? AND project_id=?", ids.Revisions[adopted.Revision], target.id).Scan(&parent); err != nil || parent != ids.Revisions[first.Revision] {
		t.Fatal("history parent not remapped", parent, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.CopyRecoveryMemoryTx(ctx, tx, actor, ids)
	tx.Rollback()
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatal("retry overwrote existing copy", err)
	}
	// Removing the independent copy leaves the original deletion and all source
	// memory rows intact; there is no shared live project identity.
	if err = db.QueryRow("SELECT revision FROM projects WHERE id=?", target.id).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+target.id, map[string]any{"confirm_name": "恢复副本", "revision": revision, "operation_id": "delete-recovered-copy"}, 200)
	var sourceCount int
	if err = db.QueryRow("SELECT COUNT(*) FROM memory_revisions WHERE project_id=?", original.id).Scan(&sourceCount); err != nil || sourceCount != 3 {
		t.Fatal("copy deletion damaged original history", sourceCount, err)
	}
}
