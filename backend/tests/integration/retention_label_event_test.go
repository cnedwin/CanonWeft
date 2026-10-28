package integration

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredPublicationEventCannotRestartPropagation(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "发布事件过期", "event-expiry")
	branchID := branch(b, project, "正史")
	id := propose(b, project, branchID, "", "event-ip", originalSnapshot())
	publish(b, project, branchID, id)
	event := publicationEvent(t, db, id)
	me := b.request("GET", "/api/me", nil, 200)
	ip := propagationService(t, db)
	if _, err := ip.PropagatePublication(ctx, event); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=?", event); err != nil {
		t.Fatal(err)
	}
	expireTransientRecord(t, db, me["id"].(string), project, "outbox", "payload", event)
	result, err := ip.PropagatePublication(ctx, event)
	if err != nil || result.State != "CANCELLED" {
		t.Fatal("expired event replay", result, err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? AND reference_id IN (SELECT id FROM retention_references WHERE owner_table='outbox' AND project_id=?)", project, project); err != nil {
		t.Fatal(err)
	}
	if _, err := ip.PropagatePublication(ctx, event); err == nil {
		t.Fatal("unproved event replay")
	}
}

func TestDeletedDiscoveryPathRetainsSourceIdentityAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "发现路径过期", "path-expiry")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.SkillDiscovery{IP: propagationService(t, db), Remote: &discoveryFixture{commit: strings.Repeat("a", 40)}}
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Keyword: "method", Enabled: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "discover")
	if err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, run.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	view, err := svc.Overview(ctx, actor, project)
	if err != nil || len(view.Candidates) != 1 {
		t.Fatal("candidate", err)
	}
	candidate := view.Candidates[0]
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: svc.IP.Workspace, Blobs: store}
	key := retention.SourceKey{Table: "skill_discovery_candidates", Primary: map[string]string{"id": candidate.ID}}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, key, "source_path"); err != nil {
		t.Fatal("path", err)
	}
	if _, err := scanner.Scan(ctx, actor.ID, project); err != nil {
		t.Fatal("rescan", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "discovery-path.zip")); err != nil {
		t.Fatal("archive", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-proof.zip")); err == nil {
		t.Fatal("unproved path accepted")
	}
}

func TestExpiredDiscoveryPathKeepsDedupAndNewUpstream(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "发现路径续用", "path-reuse")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	remote := &discoveryFixture{commit: strings.Repeat("a", 40)}
	svc := &service.SkillDiscovery{IP: propagationService(t, db), Remote: remote}
	source, err := svc.SaveSource(ctx, actor, project, "", service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Keyword: "method", Enabled: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.Start(ctx, actor, project, source.ID, "discover")
	if err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, run.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	view, err := svc.Overview(ctx, actor, project)
	if err != nil || len(view.Candidates) != 1 {
		t.Fatal("candidate", err)
	}
	old := view.Candidates[0]
	expireTransientRecord(t, db, actor.ID, project, "skill_discovery_runs", "inventory", run.ID, 2)
	view, err = svc.Overview(ctx, actor, project)
	if err != nil || len(view.Candidates) != 1 || view.Candidates[0].Retention == nil || view.Candidates[0].Path != "" {
		t.Fatal("path history", err)
	}
	for i, op := range []string{"same-commit", "new-commit"} {
		if i == 1 {
			remote.commit = strings.Repeat("b", 40)
		}
		next, err := svc.Start(ctx, actor, project, source.ID, op)
		if err != nil {
			t.Fatal(err)
		}
		if state, err := svc.Execute(ctx, next.ID); err != nil || state != "COMPLETED" {
			t.Fatal(state, err)
		}
		view, err = svc.Overview(ctx, actor, project)
		if err != nil || len(view.Candidates) != i+1 {
			t.Fatal("dedup/update", i, err)
		}
		if i == 1 && (view.Candidates[0].PreviousID != old.PackageID || view.Candidates[0].Retention != nil) {
			t.Fatal("upstream lineage lost")
		}
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Overview(ctx, actor, project); err == nil {
		t.Fatal("unproved discovery history")
	}
}
