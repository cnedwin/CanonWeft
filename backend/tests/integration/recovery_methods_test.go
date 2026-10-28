package integration

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRecoveryMethodsPreserveEditsRevocationsPresetsAndDiscovery(t *testing.T) {
	b, db := setup(t)
	source := createProject(b, "原方法库", "recovery-method-source")
	target := createProject(b, "恢复方法库", "recovery-method-target")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), WorkspaceID: me["workspace_id"].(string), Role: me["role"].(string), Username: me["username"].(string)}
	ip := &service.IP{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}}
	caps, packages := &service.Capabilities{IP: ip}, &service.Skills{IP: ip}
	ctx := context.Background()
	installed, err := caps.InstallResearch(ctx, actor, source)
	if err != nil {
		t.Fatal(err)
	}
	method, err := caps.Read(ctx, actor, source, installed.Versions[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	method.Definition.Instructions += "\n作者增加的具体写作要求。"
	edited, err := caps.Save(ctx, actor, source, service.CapabilitySave{OperationID: "edited-method", ExpectedHead: method.ID, Definition: method.Definition})
	if err != nil {
		t.Fatal(err)
	}
	if err = caps.Revoke(ctx, actor, source, method.ID, "原方法需重新审核"); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: personal-method\ndescription: Personal research notes.\nlicense: LicenseRef-Private\n---\n保留资料原始来源。"
	pkg, err := packages.Create(ctx, actor, source, "personal-package", body)
	if err != nil {
		t.Fatal(err)
	}
	child, err := packages.Edit(ctx, actor, source, pkg.ID, service.SkillEdit{OperationID: "personal-edits", ExpectedHash: pkg.Report.Hash, Changes: []service.SkillFileChange{{Path: "notes.txt", Text: "作者的专业细节与原始意见🌙"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = packages.Reference(ctx, actor, source, child.ID); err != nil {
		t.Fatal(err)
	}
	if err = packages.Revoke(ctx, actor, source, pkg.ID, "来源不再可用"); err != nil {
		t.Fatal(err)
	}
	// Simulate an old business snapshot; protected revocation remains current.
	if _, err = db.Exec("DELETE FROM skill_revocations WHERE project_id=? AND package_id=?", source, pkg.ID); err != nil {
		t.Fatal(err)
	}
	remote := &discoveryFixture{commit: strings.Repeat("a", 40)}
	discovery := &service.SkillDiscovery{IP: ip, Remote: remote}
	registered, err := discovery.SaveSource(ctx, actor, source, "", service.DiscoverySourceInput{OperationID: "method-source", Repository: "fixture/repository", Ref: "main", Path: "skills", Limit: 1, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	run, err := discovery.Start(ctx, actor, source, registered.ID, "method-discover")
	if err != nil {
		t.Fatal(err)
	}
	if state, e := discovery.Execute(ctx, run.ID); e != nil || state != "COMPLETED" {
		t.Fatal(state, e)
	}
	pending, err := discovery.Start(ctx, actor, source, registered.ID, "unfinished-discover")
	if err != nil {
		t.Fatal(err)
	}
	ids := service.RecoveryMethodIDs{SourceProject: source, TargetProject: target, Packages: map[string]string{}, Versions: map[string]string{}, Sources: map[string]string{}, Runs: map[string]string{}, Candidates: map[string]string{}}
	for table, mapping := range map[string]map[string]string{"skill_packages": ids.Packages, "capability_versions": ids.Versions, "skill_discovery_sources": ids.Sources, "skill_discovery_runs": ids.Runs, "skill_discovery_candidates": ids.Candidates} {
		rows, e := db.Query("SELECT id FROM "+table+" WHERE project_id=?", source)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				t.Fatal(e)
			}
			mapping[id] = service.NewID()
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	var revision int64
	if err = db.QueryRow("SELECT revision FROM projects WHERE id=?", source).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+source, map[string]any{"confirm_name": "原方法库", "revision": revision, "operation_id": "delete-method-source"}, 200)
	if _, err = db.Exec("CREATE TRIGGER recovery_method_fault BEFORE INSERT ON capability_preset_steps FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test method recovery fault'"); err != nil {
		t.Fatal(err)
	}
	copy := func() (service.RecoveryMethodCounts, error) {
		t.Helper()
		tx, e := db.BeginTx(ctx, &sql.TxOptions{})
		if e != nil {
			t.Fatal(e)
		}
		counts, copyErr := ip.CopyRecoveryMethodsTx(ctx, tx, actor.ID, ids)
		if e = tx.Commit(); e != nil {
			t.Fatal(e)
		}
		return counts, copyErr
	}
	if counts, e := copy(); e == nil || !strings.Contains(e.Error(), "test method recovery fault") || counts != (service.RecoveryMethodCounts{}) {
		t.Fatal("partial restore accepted", counts, e)
	}
	if _, err = db.Exec("DROP TRIGGER recovery_method_fault"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRow("SELECT COUNT(*) FROM skill_packages WHERE project_id=?", target).Scan(&count); err != nil || count != 0 {
		t.Fatal("half-copy persisted", count, err)
	}
	counts, err := copy()
	if err != nil || counts.Packages != len(ids.Packages) || counts.Versions != len(ids.Versions) || counts.Runs != 2 || counts.Candidates != 1 {
		t.Fatal(counts, err)
	}
	file, err := packages.File(ctx, actor, target, ids.Packages[child.ID], "notes.txt")
	if err != nil || file.Text != "作者的专业细节与原始意见🌙" {
		t.Fatal("private edits lost", file, err)
	}
	copyPackage, err := packages.Read(ctx, actor, target, ids.Packages[child.ID])
	if err != nil || copyPackage.Available || copyPackage.ParentID != ids.Packages[pkg.ID] {
		t.Fatal("protected source revocation bypassed", copyPackage, err)
	}
	// Repeated installation must return the copied preset versions while keeping
	// the newer author-edited head; it must never create a duplicate workflow.
	reinstalled, err := caps.InstallResearch(ctx, actor, target)
	if err != nil || reinstalled.RootID != ids.Versions[installed.RootID] {
		t.Fatal("preset receipts not recovered", reinstalled, err)
	}
	var head string
	if err = db.QueryRow("SELECT version_id FROM capability_heads WHERE project_id=? AND capability_key=?", target, method.Definition.Key).Scan(&head); err != nil || head != ids.Versions[edited.ID] {
		t.Fatal("preset overwrote author edits", head, err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM capability_versions WHERE project_id=?", target).Scan(&count); err != nil || count != len(ids.Versions) {
		t.Fatal("duplicate capabilities", count, err)
	}
	manifest, err := caps.Manifest(ctx, actor, target, ids.Versions[installed.RootID])
	if err != nil || manifest.Qualified || manifest.TrialReady {
		t.Fatal("revoked capability regained execution", manifest, err)
	}
	overview, err := discovery.Overview(ctx, actor, target)
	if err != nil || len(overview.Sources) != 1 || overview.Sources[0].Enabled || len(overview.Candidates) != 1 {
		t.Fatal("discovery source not preserved safely", overview, err)
	}
	if stopped, controlErr := discovery.Control(ctx, actor, target, ids.Runs[pending.ID], "resume"); controlErr != nil || stopped.State != "CANCELLED" {
		t.Fatal("old scan could resume", stopped, controlErr)
	}
	for _, row := range overview.Runs {
		if row.Source.ID != ids.Sources[registered.ID] || row.State == "QUEUED" || row.State == "RUNNING" || row.State == "PAUSED" {
			t.Fatal("old scan still runnable", row)
		}
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=?", target).Scan(&count); err != nil || count != 0 {
		t.Fatal("old tasks copied", count, err)
	}
	resumedSource, err := discovery.SaveSource(ctx, actor, target, ids.Sources[registered.ID], service.DiscoverySourceInput{OperationID: "enable-recovered-source", Repository: registered.Repository, Ref: registered.Ref, Path: registered.Path, Limit: registered.Limit, ExpectedRevision: registered.Revision, Enabled: true})
	if err != nil {
		t.Fatal("recovered source cannot be enabled", err)
	}
	fresh, err := discovery.Start(ctx, actor, target, resumedSource.ID, "scan-recovered-source")
	if err != nil {
		t.Fatal(err)
	}
	if state, executeErr := discovery.Execute(ctx, fresh.ID); executeErr != nil || state != "COMPLETED" {
		t.Fatal("fresh discovery failed", state, executeErr)
	}
	overview, err = discovery.Overview(ctx, actor, target)
	if err != nil || len(overview.Candidates) != 1 {
		t.Fatal("recovery lost upstream deduplication", overview, err)
	}
	if _, err = backup.Create(ctx, db, ip.Blobs, filepath.Join(t.TempDir(), "recovered-methods.zip")); err != nil {
		t.Fatal("recovered methods cannot be archived", err)
	}
}
