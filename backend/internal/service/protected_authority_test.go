package service

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/storage"
)

func authorityWorkspace(t *testing.T, provision bool) (*Workspace, domain.Actor, *sql.DB, string) {
	t.Helper()
	dsn := os.Getenv("STUDIO_AUTHORITY_TEST_ADMIN_DSN")
	if dsn == "" {
		t.Skip("isolated authority test requires admin DSN")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("invalid isolated configuration")
	}
	cfg.DBName = ""
	cfg.ParseTime = true
	admin, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	name := "studio_source_test_" + NewID()
	schema := "studio_authority_test_" + NewID()
	if _, err = admin.Exec("CREATE DATABASE `" + name + "` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, entry := range []struct{ name, prefix string }{{name, "studio_source_test_"}, {schema, "studio_authority_test_"}} {
			suffix := strings.TrimPrefix(entry.name, entry.prefix)
			if len(suffix) != 32 {
				t.Fatal("unsafe test cleanup")
			}
			if _, err := hex.DecodeString(suffix); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec("DROP DATABASE IF EXISTS `" + entry.name + "`"); err != nil {
				t.Error(err)
			}
		}
	})
	cfg.DBName = name
	db, err := sql.Open("mysql", cfg.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = storage.RunMigrations(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", schema)
	if provision {
		if _, err = authority.Provision(context.Background(), admin, schema, name); err != nil {
			t.Fatal(err)
		}
	}
	svc := &Workspace{DB: db}
	if err = svc.Setup(context.Background(), "authority-owner", "test-password-123456", "authority tests"); err != nil {
		t.Fatal(err)
	}
	token, err := svc.Login(context.Background(), "authority-owner", "test-password-123456")
	if err != nil {
		t.Fatal(err)
	}
	actor, err := svc.Actor(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return svc, actor, admin, schema
}

func TestProtectedRevocationSurvivesBusinessRollback(t *testing.T) {
	svc, actor, _, _ := authorityWorkspace(t, true)
	ctx := context.Background()
	p, err := svc.CreateProject(ctx, actor.ID, "protected revocations", NewID())
	if err != nil {
		t.Fatal(err)
	}
	parent, child := NewID(), NewID()
	hash := strings.Repeat("a", 64)
	for _, entry := range []struct{ id, parent string }{{parent, ""}, {child, parent}} {
		if _, err = svc.DB.Exec("INSERT INTO skill_packages(id,project_id,parent_id,bundle_hash,report,source,created_by) VALUES(?,?,?,?,?,'{}',?)", entry.id, p.ID, entry.parent, hash, `{"hash":"`+hash+`"}`, actor.ID); err != nil {
			t.Fatal(err)
		}
	}
	ip := &IP{Workspace: svc}
	if err = (&Skills{IP: ip}).Revoke(ctx, actor, p.ID, parent, "parent withdrawn"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DB.Exec("DELETE FROM skill_revocations WHERE project_id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	available, err := skillAvailable(ctx, tx, p.ID, child)
	tx.Rollback()
	if err != nil || available {
		t.Fatal("old business data revived revoked skill ancestor", available, err)
	}
	definition := domain.CapabilityDefinition{Kind: "agent", Key: "authority-test", Name: "Authority test", Description: "Test", Instructions: "Propose only", Role: "critic", InputSchema: json.RawMessage(`{"type":"object"}`), OutputSchema: json.RawMessage(`{"type":"object"}`)}
	capabilities := &Capabilities{IP: ip}
	version, err := capabilities.Save(ctx, actor, p.ID, CapabilitySave{OperationID: NewID(), Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	if err = capabilities.Revoke(ctx, actor, p.ID, version.ID, "capability withdrawn"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DB.Exec("DELETE FROM capability_revocations WHERE project_id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	tx, err = svc.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	available, err = capabilityAvailable(ctx, tx, p.ID, version.ID)
	tx.Rollback()
	if err != nil || available {
		t.Fatal("old business data revived revoked capability", available, err)
	}
}

func TestProjectCreationFailsClosedWithoutProtectedAuthority(t *testing.T) {
	svc, actor, _, _ := authorityWorkspace(t, false)
	if _, err := svc.CreateProject(context.Background(), actor.ID, "must not commit", NewID()); err == nil {
		t.Fatal("project committed without protected authority")
	}
	var count int
	if err := svc.DB.QueryRow("SELECT COUNT(*) FROM projects").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed authority left business write", count, err)
	}
}

func TestProjectDeletionRejectsWorkspaceReaderWhoStillOwnsProject(t *testing.T) {
	svc, owner, _, _ := authorityWorkspace(t, true)
	ctx := context.Background()
	editor := NewID()
	if _, err := svc.DB.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'downgraded-owner','unused','editor')", editor, owner.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	project, err := svc.CreateProject(ctx, editor, "reader-owned project", NewID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DB.Exec("UPDATE users SET role='reader' WHERE id=?", editor); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.UpdateProject(ctx, editor, project.ID, "blocked rename", project.Revision); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("fixture lost existing read-only write restriction", err)
	}
	if err = svc.DeleteProject(ctx, editor, project.ID, project.Name, project.Revision, NewID()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("read-only actor deleted a project", err)
	}
	if _, err = svc.Project(ctx, owner, project.ID); err != nil {
		t.Fatal("rejected delete modified project", err)
	}
}

func TestDeletedProjectBlocksReadsTasksAndRollback(t *testing.T) {
	svc, actor, admin, schema := authorityWorkspace(t, true)
	ctx := context.Background()
	p, err := svc.CreateProject(ctx, actor.ID, "删除测试", NewID())
	if err != nil {
		t.Fatal(err)
	}
	blobs := storage.LocalBlobs{Root: t.TempDir()}
	ip := &IP{Workspace: svc, Blobs: blobs}
	branch, err := ip.CreateBranch(ctx, actor, p.ID, "main", "", "", NewID())
	if err != nil {
		t.Fatal(err)
	}
	resource, err := ip.CreateResource(ctx, actor, p.ID, ResourceInput{BranchID: branch.ID, Name: "private source", Kind: "source", OperationID: NewID()})
	if err != nil {
		t.Fatal(err)
	}
	object, err := blobs.PutObject(ctx, p.ID, strings.NewReader("private retained content"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DB.Exec("UPDATE resources SET content_hash=? WHERE id=?", object.Hash, resource.ID); err != nil {
		t.Fatal(err)
	}
	read, err := ip.ReadResource(ctx, actor, p.ID, resource.ID)
	if err != nil || read.Text != "private retained content" {
		t.Fatal("live private blob fixture invalid", err)
	}
	candidate, err := ip.Propose(ctx, actor, p.ID, branch.ID, "", NewID(), domain.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	tasks := &Tasks{IP: ip}
	task, err := tasks.StartReview(ctx, actor, p.ID, branch.ID, candidate.ID, NewID())
	if err != nil {
		t.Fatal(err)
	}
	editor := NewID()
	if _, err = svc.DB.Exec("INSERT INTO users(id,workspace_id,username,password_hash,role) VALUES(?,?,'non-owner','unused','editor')", editor, actor.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DB.Exec("INSERT INTO project_members(project_id,user_id,role) VALUES(?,?,'editor')", p.ID, editor); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteProject(ctx, editor, p.ID, p.Name, p.Revision, NewID()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("non-owner deleted project", err)
	}
	operation := NewID()
	if err = svc.DeleteProject(ctx, actor.ID, p.ID, "wrong name", p.Revision, operation); !errors.Is(err, domain.ErrValidation) {
		t.Fatal("name confirmation bypass", err)
	}
	if err = svc.DeleteProject(ctx, actor.ID, p.ID, p.Name, p.Revision+1, operation); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale revision bypass", err)
	}
	if err = svc.DeleteProject(ctx, actor.ID, p.ID, p.Name, p.Revision, operation); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteProject(ctx, actor.ID, p.ID, p.Name, p.Revision, operation); err != nil {
		t.Fatal("lost receipt cannot reconcile", err)
	}
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", "studio_authority_test_"+NewID())
	if err = svc.DeleteProject(ctx, actor.ID, p.ID, p.Name, p.Revision, operation); err == nil {
		t.Fatal("deletion receipt accepted while current authority unavailable")
	}
	t.Setenv("STUDIO_AUTHORITY_SCHEMA", schema)
	if _, err = svc.Project(ctx, actor, p.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("deleted read allowed", err)
	}
	projects, err := svc.Projects(ctx, actor)
	if err != nil || len(projects) != 0 {
		t.Fatal("deleted project listed", err)
	}
	if _, err = tasks.List(ctx, actor, p.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("deleted task access allowed", err)
	}
	if _, err = tasks.StartReview(ctx, actor, p.ID, NewID(), NewID(), NewID()); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("deleted task creation allowed", err)
	}
	if state, err := tasks.Inspect(ctx, task.ID); err != nil || state != "BLOCKED" {
		t.Fatal("worker continued deleted project", state, err)
	}
	if _, err = ip.ReadResource(ctx, actor, p.ID, resource.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("private blob access survived deletion", err)
	}
	knowledge := &Knowledge{IP: ip}
	if _, err = knowledge.Search(ctx, actor, p.ID, branch.ID, SearchInput{Query: "private", Purpose: "research", Limit: 5}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("retrieval survived deletion", err)
	}
	if raw, err := blobs.ReadObject(ctx, p.ID, object.Hash); err != nil || string(raw) != "private retained content" {
		t.Fatal("T0 unexpectedly purged retained object", err)
	}
	if _, err = svc.DB.Exec("UPDATE projects SET deleted_at=NULL WHERE id=?", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Project(ctx, actor, p.ID); !errors.Is(err, domain.ErrForbidden) {
		t.Fatal("restored old business row reopened project", err)
	}
	var source authority.Source
	if err = svc.DB.QueryRow("SELECT @@server_uuid,DATABASE()").Scan(&source.ServerUUID, &source.Database); err != nil {
		t.Fatal(err)
	}
	state, err := (authority.Ledger{Schema: schema}).Current(ctx, admin, source)
	if err != nil || state.Projects[p.ID] {
		t.Fatal("protected tombstone missing", err)
	}
	var audits, receipts int
	if err = svc.DB.QueryRow("SELECT COUNT(*) FROM audit_events WHERE action='project.delete' AND resource_id=?", p.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if err = svc.DB.QueryRow("SELECT COUNT(*) FROM operations WHERE operation_id=?", operation).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || receipts != 1 {
		t.Fatal("delete replay duplicated evidence", audits, receipts)
	}
}
