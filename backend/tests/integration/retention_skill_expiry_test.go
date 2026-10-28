package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredSkillHistoryRejectsReuseAndAllowsNewPackage(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "方法材料过期", "skill-expiry")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
	svc := &service.Skills{IP: propagationService(t, db)}
	body := "---\nname: source-review\ndescription: Use when checking source evidence.\nlicense: LicenseRef-Private\n---\nPreserve evidence and propose a candidate."
	old, err := svc.Create(ctx, actor, project, "old-skill", body)
	if err != nil {
		t.Fatal(err)
	}
	expireTransientRecord(t, db, actor.ID, project, "skill_packages", "report", old.ID)
	list, err := svc.List(ctx, actor, project)
	if err != nil || len(list) != 1 || list[0].Retention == nil || list[0].Available || list[0].Report.Name != "" {
		t.Fatal("expired history", err)
	}
	if _, err := svc.Reference(ctx, actor, project, old.ID); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired reference", err)
	}
	if _, err := svc.File(ctx, actor, project, old.ID, "SKILL.md"); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired file", err)
	}
	if _, err := svc.Create(ctx, actor, project, "old-skill", body); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired retry", err)
	}
	if _, err := svc.Edit(ctx, actor, project, old.ID, service.SkillEdit{OperationID: "edit-expired", ExpectedHash: old.Report.Hash, Changes: []service.SkillFileChange{{Path: "SKILL.md", Text: body + " Revised."}}}); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("expired edit", err)
	}
	fresh, err := svc.Create(ctx, actor, project, "fresh-skill", body+" Preserve exact anchors.")
	if err != nil || fresh.ID == old.ID || fresh.Retention != nil {
		t.Fatal("fresh package", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(ctx, actor, project); err == nil {
		t.Fatal("unproved expired history accepted")
	}
}

func TestDeletedSkillExpiryPreservesConsumersAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "保存方法清理", "saved-skill-expiry")
	branchID := branch(b, project, "PRIVATE_BRANCH_CANARY")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string)}
	svc := &service.Skills{IP: propagationService(t, db)}
	var content bytes.Buffer
	z := zip.NewWriter(&content)
	f, err := z.Create("SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte("---\nname: source-review\ndescription: Use when checking source evidence.\nlicense: LicenseRef-Private\n---\nPreserve evidence and propose a candidate.")); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	pkg, err := svc.Import(ctx, actor, project, "archive-method", content.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	method := capabilityDefinition("skill", "saved-method")
	method["package_id"], method["package_hash"] = pkg.ID, pkg.Report.Hash
	root := "/api/projects/" + project + "/capabilities"
	m := b.request("POST", root, map[string]any{"operation_id": "method", "definition": method}, 201)
	agent := capabilityDefinition("agent", "saved-agent")
	agent["role"], agent["dependencies"] = "bookkeeper", []any{capabilityRef(m)}
	a := b.request("POST", root, map[string]any{"operation_id": "agent", "definition": agent}, 201)
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: svc.IP.Workspace, Blobs: store}
	packageKey := retention.SourceKey{Table: "skill_packages", Primary: map[string]string{"id": pkg.ID}}
	methodKey := retention.SourceKey{Table: "capability_versions", Primary: map[string]string{"id": m["id"].(string)}}
	agentKey := retention.SourceKey{Table: "capability_versions", Primary: map[string]string{"id": a["id"].(string)}}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, packageKey, "report"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("method did not protect package", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, methodKey, "definition"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("agent did not protect method", err)
	}
	for _, key := range []retention.SourceKey{agentKey, methodKey} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, key, "definition"); err != nil {
			t.Fatal("definition", err)
		}
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, packageKey, "source"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("archive identity did not protect source", err)
	}
	for _, field := range []string{"report", "source.archive_hash", "source", "bundle_hash"} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, packageKey, field); err != nil {
			t.Fatal(field, err)
		}
		if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "skill.zip")); err != nil {
			t.Fatal("partial archive", field, err)
		}
	}
	for _, key := range []retention.SourceKey{agentKey, methodKey} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, key, "capability_key"); err != nil {
			t.Fatal("version key", err)
		}
	}
	for _, name := range []string{"saved-method", "saved-agent"} {
		key := retention.SourceKey{Table: "capability_heads", Primary: map[string]string{"project_id": project, "capability_key": name}}
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, key, "capability_key"); err != nil {
			t.Fatal("head key", err)
		}
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, retention.SourceKey{Table: "branches", Primary: map[string]string{"id": branchID}}, "name"); err != nil {
		t.Fatal("branch name", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "labels.zip")); err != nil {
		t.Fatal("labels archive", err)
	}
	if _, err := scanner.Scan(ctx, actor.ID, project); err != nil {
		t.Fatal("rescan", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? LIMIT 1", project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-proof.zip")); err == nil {
		t.Fatal("unproved archive accepted")
	}
}
