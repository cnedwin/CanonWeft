package integration

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionDiscoveryCapturesSourcesCandidatesAndAtomicDiagnostics(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "发现来源记录", "discovery-retention")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.SkillDiscovery{IP: &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}, Remote: &discoveryFixture{commit: strings.Repeat("a", 40)}}
	ctx := context.Background()
	installSourceRegistrationFixture(t, db)
	config := service.DiscoverySourceInput{OperationID: "source", Repository: "fixture/repository", Ref: "main", Path: "skills", Keyword: "method", Enabled: true, Limit: 1}
	source, err := svc.SaveSource(ctx, actor, project, "", config)
	if err != nil {
		t.Fatal(err)
	}
	original := capturedVersion(t, db, project, "skill_discovery_sources", source.ID, "keyword")
	run, err := svc.Start(ctx, actor, project, source.ID, "start")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := capturedVersion(t, db, project, "skill_discovery_runs", run.ID, "source_snapshot")
	if len(snapshot.Reference.Sources) != 4 {
		t.Fatal("missing original configuration fields", snapshot)
	}
	if state, err := svc.Execute(ctx, run.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	stored, err := svc.Overview(ctx, actor, project)
	if err != nil || len(stored.Candidates) != 1 {
		t.Fatal(stored, err)
	}
	candidate := stored.Candidates[0]
	capturedVersion(t, db, project, "skill_discovery_runs", run.ID, "inventory")
	capturedVersion(t, db, project, "skill_discovery_candidates", candidate.ID, "source_path")
	pack := capturedVersion(t, db, project, "skill_packages", candidate.PackageID, "bundle_hash")
	if pack.Reference.ContentRoot {
		t.Fatal("remote candidate became authored content")
	}
	svc.Remote.(*discoveryFixture).commit = strings.Repeat("b", 40)
	next, err := svc.Start(ctx, actor, project, source.ID, "next-commit")
	if err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, next.ID); err != nil || state != "COMPLETED" {
		t.Fatal("upstream update", state, err)
	}
	stored, err = svc.Overview(ctx, actor, project)
	if err != nil || len(stored.Candidates) != 2 || stored.Candidates[0].PreviousID != candidate.PackageID {
		t.Fatal("previous package identity", stored, err)
	}
	active, err := svc.Start(ctx, actor, project, source.ID, "active")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER reject_discovery_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='skill_discovery_runs.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected discovery diagnostic'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_discovery_reason") })
	config.OperationID, config.ExpectedRevision, config.Keyword = "change", source.Revision, "new method"
	if _, err := svc.SaveSource(ctx, actor, project, source.ID, config); err == nil {
		t.Fatal("configuration committed without blocked-run proof")
	}
	var keyword, state string
	if err := db.QueryRow("SELECT keyword FROM skill_discovery_sources WHERE id=?", source.ID).Scan(&keyword); err != nil || keyword != "method" {
		t.Fatal("partial configuration update", keyword, err)
	}
	if err := db.QueryRow("SELECT state FROM skill_discovery_runs WHERE id=?", active.ID).Scan(&state); err != nil || state != "QUEUED" {
		t.Fatal("partial blocked state", state, err)
	}
	if _, err = db.Exec("DROP TRIGGER reject_discovery_reason"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.SaveSource(ctx, actor, project, source.ID, config); err != nil {
		t.Fatal(err)
	}
	changed := capturedVersion(t, db, project, "skill_discovery_sources", source.ID, "keyword")
	if changed.Reference.Revision != original.Reference.Revision+1 {
		t.Fatal("configuration history missing")
	}
	if report, err := svc.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("historical discovery inventory", report, err)
	}
	retained := capturedVersion(t, db, project, "skill_discovery_runs", run.ID, "source_snapshot")
	if !slices.Equal(retained.Reference.Sources, snapshot.Reference.Sources) || retained.Reference.Revision != snapshot.Reference.Revision {
		t.Fatal("old snapshot rebound to new configuration")
	}
	capturedVersion(t, db, project, "skill_discovery_runs", active.ID, "reason")
	// Cancellation and failure diagnostics do not require packages or remote IO.
	svc.IP.Blobs = nil
	cancel, err := svc.Start(ctx, actor, project, source.ID, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Control(ctx, actor, project, cancel.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Fail(ctx, cancel.ID); err != nil || state != "CANCELLED" {
		t.Fatal("late failure replaced cancellation", state, err)
	}
	paused, err := svc.Start(ctx, actor, project, source.ID, "paused")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Control(ctx, actor, project, paused.ID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE users SET role='reader' WHERE id=?", actor.ID); err != nil {
		t.Fatal(err)
	}
	if state, err := svc.Execute(ctx, paused.ID); err != nil || state != "BLOCKED" {
		t.Fatal("revocation while paused", state, err)
	}
	capturedVersion(t, db, project, "skill_discovery_runs", paused.ID, "reason")
}
