package integration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryKeepsExpiredSessionAndAdoptedMemory(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	name := "长期记忆保留"
	project := createProject(b, name, "recover-expired-session")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "主题来源")
	release := prepareRelease(b, project, branchID, "", "", "memory-source", []map[string]any{{"resource_id": rid, "text": "守护亲人是故事的主题。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	source := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
	ip := propagationService(t, db)
	svc := &service.Memory{IP: ip}
	session, err := svc.SaveSession(ctx, actor, project, branchID, service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "临时会话", Notes: "已经清理的临时推理"}})
	if err != nil {
		t.Fatal(err)
	}
	item, err := svc.Save(ctx, actor, project, branchID, service.MemoryInput{OperationID: "memory", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Control: true, Text: "守护亲人", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Transition(ctx, actor, project, branchID, item.ID, "adopt", service.MemoryAction{OperationID: "adopt", ExpectedRevision: item.Revision}); err != nil {
		t.Fatal(err)
	}
	expireTransientRecord(t, db, actor.ID, project, "memory_sessions", "document", session.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for i := 0; i < 2; i++ {
		if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
			t.Fatal(err)
		}
		copy, err := ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: name, Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()})
		if err != nil {
			t.Fatal("expired session blocked recovery", err)
		}
		branches, err := ip.Branches(ctx, actor, copy.ID)
		if err != nil || len(branches) != 1 {
			t.Fatal(err)
		}
		var sessionID string
		if err = db.QueryRow("SELECT id FROM memory_sessions WHERE project_id=?", copy.ID).Scan(&sessionID); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.ReadSession(ctx, actor, copy.ID, branches[0].ID, sessionID); !errors.Is(err, domain.ErrMaterialExpired) {
			t.Fatal("expired session revived", err)
		}
		pack, err := svc.Compile(ctx, actor, copy.ID, branches[0].ID, service.MemoryCompileInput{Purpose: "author", Budget: 65536})
		if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.Text != "守护亲人" {
			t.Fatal("adopted memory lost", err)
		}
		if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "session-memory.zip")); err != nil {
			t.Fatal("session archive rejected", err)
		}
		project = copy.ID
	}
	if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, name, 1, "request-v2:"+service.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	input := service.ProjectRecoveryInput{Name: "缺证据不得创建", Confirmation: name, Revision: 2, OperationID: "request-v2:" + service.NewID()}
	if _, err = ip.RecoverProject(ctx, actor.ID, project, input); err == nil {
		t.Fatal("session marker without independent evidence accepted")
	}
	var partial int
	if err = db.QueryRow("SELECT COUNT(*) FROM projects WHERE name=?", input.Name).Scan(&partial); err != nil || partial != 0 {
		t.Fatal("missing evidence left partial recovery", partial, err)
	}
}
