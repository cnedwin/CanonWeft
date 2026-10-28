package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestRecoveryKeepsFullyAndPartiallyExpiredMemoryHistory(t *testing.T) {
	for _, partial := range []bool{false, true} {
		name := "fully"
		if partial {
			name = "partially"
		}
		t.Run(name, func(t *testing.T) {
			b, db := setup(t)
			ctx := context.Background()
			projectName := "过期候选记忆恢复"
			project := createProject(b, projectName, "recover-expired-memory")
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
			session, err := svc.SaveSession(ctx, actor, project, branchID, service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "继续写作"}})
			if err != nil {
				t.Fatal(err)
			}
			input := service.MemoryInput{OperationID: "old-memory", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Text: "不可复活的旧推测", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}}
			old, err := svc.Save(ctx, actor, project, branchID, input)
			if err != nil {
				t.Fatal(err)
			}
			input.OperationID, input.Item.Text, input.Item.Control = "live-memory", "守护亲人", true
			live, err := svc.Save(ctx, actor, project, branchID, input)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = svc.Transition(ctx, actor, project, branchID, live.ID, "adopt", service.MemoryAction{OperationID: "adopt", ExpectedRevision: live.Revision}); err != nil {
				t.Fatal(err)
			}
			installCompleteRetentionFixture(t, db)
			for table, id := range map[string]string{"memory_items": old.ID, "memory_revisions": old.Revision} {
				var raw string
				if err := db.QueryRow("SELECT document FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
					t.Fatal(err)
				}
				owner, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
				ref, _ := retention.ReferenceID(project, table, owner, "document")
				hash := sha256.Sum256([]byte(raw))
				if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,'document','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, table, owner); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
					t.Fatal(err)
				}
			}
			store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
			job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
			if err != nil || job.Expiring != 2 {
				t.Fatal("unexpected cleanup", job, err)
			}
			if _, err = scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
				t.Fatal(err)
			}
			state, err := scanner.AdvanceJob(ctx, job.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !partial {
				for i := 0; i < 10 && state != "COMPLETE"; i++ {
					state, err = scanner.AdvanceJob(ctx, job.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
				if state != "COMPLETE" {
					t.Fatal(state)
				}
			}
			for i := 0; i < 2; i++ {
				if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, projectName, 1, "request-v2:"+service.NewID()); err != nil {
					t.Fatal(err)
				}
				recoveryInput := service.ProjectRecoveryInput{Name: projectName, Confirmation: projectName, Revision: 2, OperationID: "request-v2:" + service.NewID()}
				if i == 0 {
					var before, after int
					if err = db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&before); err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(`CREATE TRIGGER memory_recovery_fault BEFORE INSERT ON operations FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='late recovery failure'`); err != nil {
						t.Fatal(err)
					}
					if _, err = ip.RecoverProject(ctx, actor.ID, project, recoveryInput); err == nil {
						t.Fatal("late failure ignored")
					}
					if err = db.QueryRow("SELECT COUNT(*) FROM projects").Scan(&after); err != nil || before != after {
						t.Fatal("partial copy left behind", before, after, err)
					}
					if _, err = db.Exec("DROP TRIGGER memory_recovery_fault"); err != nil {
						t.Fatal(err)
					}
				}
				copy, err := ip.RecoverProject(ctx, actor.ID, project, recoveryInput)
				if err != nil {
					t.Fatal("erased candidate blocked novel recovery", err)
				}
				retry, err := ip.RecoverProject(ctx, actor.ID, project, recoveryInput)
				if err != nil || retry.ID != copy.ID {
					t.Fatal("retry duplicated recovery", err)
				}
				branches, err := ip.Branches(ctx, actor, copy.ID)
				if err != nil || len(branches) != 1 {
					t.Fatal(err)
				}
				list, err := svc.List(ctx, actor, copy.ID, branches[0].ID)
				if err != nil || len(list.Items) != 1 || len(list.Expired) != 1 {
					t.Fatal("memory history missing", list, err)
				}
				var expiredRevision string
				if err = db.QueryRow("SELECT head_revision FROM memory_items WHERE id=? AND project_id=?", list.Expired[0].ID, copy.ID).Scan(&expiredRevision); err != nil {
					t.Fatal(err)
				}
				if _, err = svc.Transition(ctx, actor, copy.ID, branches[0].ID, list.Expired[0].ID, "adopt", service.MemoryAction{OperationID: "request-v2:" + service.NewID(), ExpectedRevision: expiredRevision}); !errors.Is(err, domain.ErrMaterialExpired) {
					t.Fatal("expired candidate revived", err)
				}
				pack, err := svc.Compile(ctx, actor, copy.ID, branches[0].ID, service.MemoryCompileInput{Purpose: "author", Budget: 65536})
				if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.Text != "守护亲人" {
					t.Fatal("adopted theme lost", err)
				}
				if _, err = backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "memory-expiry.zip")); err != nil {
					t.Fatal("memory archive failed", err)
				}
				project = copy.ID
			}
			if err = ip.Workspace.DeleteProject(ctx, actor.ID, project, projectName, 1, "request-v2:"+service.NewID()); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
				t.Fatal(err)
			}
			if _, err = ip.RecoverProject(ctx, actor.ID, project, service.ProjectRecoveryInput{Name: "不得创建的记忆副本", Confirmation: projectName, Revision: 2, OperationID: "request-v2:" + service.NewID()}); err == nil {
				t.Fatal("missing independent memory evidence accepted")
			}
			var partialCopy int
			if err = db.QueryRow("SELECT COUNT(*) FROM projects WHERE name='不得创建的记忆副本'").Scan(&partialCopy); err != nil || partialCopy != 0 {
				t.Fatal("failed proof left a copy", partialCopy, err)
			}
		})
	}
}
