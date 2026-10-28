package integration

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRecoveryResourcesRetainHistoryImportsAndNonExecutableAIOrigins(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, gen, actor, source, job := generationFixture(t, transport)
	me := b.request("GET", "/api/me", nil, 200)
	actor.WorkspaceID, actor.Role, actor.Username = me["workspace_id"].(string), me["role"].(string), me["username"].(string)
	ctx := context.Background()
	db := gen.IP.Workspace.DB
	ip := gen.IP
	if _, err := gen.Start(ctx, actor, source, job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := gen.Execute(ctx, job.ID); err != nil {
		t.Fatal(err)
	}
	textID := resource(b, source, job.BranchID, "正文历史")
	first := prepareRelease(b, source, job.BranchID, "", "", "recovery-body-v1", []map[string]any{{"resource_id": textID, "text": "正文第一稿"}})
	releaseAdopt(b, source, first)
	releasePublish(b, source, first)
	second := prepareRelease(b, source, job.BranchID, "", "", "recovery-body-v2", []map[string]any{{"resource_id": textID, "expected_head": first, "text": "正文第二稿"}})
	releaseAdopt(b, source, second)
	releasePublish(b, source, second)
	if _, err := ip.SaveResourceDraft(ctx, actor, source, textID, service.ResourceDraft{BaseHead: second, Text: "作者尚未采用的第三稿", Policy: &domain.KnowledgePolicy{Space: "working", Visibility: "author"}}); err != nil {
		t.Fatal(err)
	}
	aiID := resource(b, source, job.BranchID, "AI来源候选")
	ai := prepareRelease(b, source, job.BranchID, "", "", "recovery-ai-candidate", []map[string]any{{"resource_id": aiID, "text": "测试候选", "origin": domain.ArtifactOriginInput{GenerationID: job.ID}}})
	imported, err := ip.ImportSource(ctx, actor, source, job.BranchID, "研究.txt", "recovery-import", []byte("待采用的题材专业资料"))
	if err != nil {
		t.Fatal(err)
	}
	importCandidate, err := ip.ImportCandidate(ctx, actor, source, job.BranchID, imported.ID, service.ImportCandidateInput{Text: imported.Report.Text, OperationID: "recovery-import-candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ip.SaveImportDraft(ctx, actor, source, job.BranchID, imported.ID, service.ImportDraft{Content: domain.ImportDraftContent{Text: "作者补充的资料意见", OperationID: "old-import-draft"}}); err != nil {
		t.Fatal(err)
	}
	// A second resource pins a historical body together with its source policy.
	catalog, err := ip.DependencyCatalog(ctx, actor, source)
	if err != nil {
		t.Fatal(err)
	}
	var pin domain.DependencyRef
	for _, entry := range catalog {
		if entry.Ref.ID == textID {
			pin = entry.Ref
		}
	}
	if pin.ID == "" {
		t.Fatal("fixture dependency missing")
	}
	dependentID := resource(b, source, job.BranchID, "依赖正文的章节说明")
	dependent := prepareRelease(b, source, job.BranchID, "", "", "recovery-dependent", []map[string]any{{"resource_id": dependentID, "text": "基于正文的章节说明", "dependencies": domain.DependencyPolicy{Inputs: []domain.Dependency{{Pinned: true, Source: pin, Kind: "evidence", Reason: "保留引用版本"}}}}})
	releaseAdopt(b, source, dependent)
	releasePublish(b, source, dependent)
	planned := prepareRelease(b, source, job.BranchID, "", "", "recovery-planned-body", []map[string]any{{"resource_id": textID, "expected_head": second, "text": "拟议的正文修改"}})
	plan, err := ip.CreateChangePlan(ctx, actor, source, service.ChangePlanInput{BranchID: job.BranchID, ReleaseID: planned, OperationID: "recovery-change-plan"})
	if err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err = db.QueryRow("SELECT id FROM outbox WHERE project_id=? AND resource_id=? AND kind='release.published'", source, second).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	propagated, err := ip.PropagatePublication(ctx, eventID)
	if err != nil {
		t.Fatal(err)
	}
	firstRead, err := ip.ReadRelease(ctx, actor, source, first)
	if err != nil {
		t.Fatal(err)
	}
	groupID := strings.Repeat("a", 32)
	privatePatch := domain.PatchDraftContent{OperationID: "unfinished-source-patch", SelectedResourceID: textID, Snippet: "正文第一稿", Replacement: "尚未完成的作者意见🌙", Reason: "保留原版本定位", Group: domain.TextPatchGroup{ID: groupID, Members: []string{textID}, Patches: []domain.TextPatch{{ID: strings.Repeat("b", 32), GroupID: groupID, ResourceID: textID, BaseRevision: first, BaseHash: firstRead.Manifest.Artifacts[0].Hash, StartRune: 0, EndRune: len([]rune("正文第一稿")), ExpectedSliceHash: firstRead.Manifest.Artifacts[0].Hash, Replacement: "修改中的第一稿"}}}}
	if _, err = ip.SavePatchDraft(ctx, actor, source, job.BranchID, service.PatchDraft{Content: &privatePatch}); err != nil {
		t.Fatal(err)
	}
	target := createProject(b, "正文恢复副本", "recovery-resource-target")
	ids := service.RecoveryResourceIDs{RecoveryIPIDs: service.RecoveryIPIDs{SourceProject: source, TargetProject: target, Branches: map[string]string{job.BranchID: service.NewID()}, Revisions: map[string]string{}}, Resources: map[string]string{}, Releases: map[string]string{}, Imports: map[string]string{imported.ID: service.NewID()}, Skills: map[string]string{}}
	for _, id := range []string{textID, aiID, importCandidate.Manifest.Artifacts[0].ResourceID, dependentID} {
		ids.Resources[id] = service.NewID()
	}
	for _, id := range []string{first, second, ai, importCandidate.ID, dependent, planned} {
		ids.Releases[id] = service.NewID()
	}
	var name string
	var revision int64
	if err = db.QueryRow("SELECT name,revision FROM projects WHERE id=?", source).Scan(&name, &revision); err != nil {
		t.Fatal(err)
	}
	b.request("DELETE", "/api/projects/"+source, map[string]any{"confirm_name": name, "revision": revision, "operation_id": "delete-resource-source"}, 200)
	copyIP := func(tx *sql.Tx) {
		t.Helper()
		if _, e := ip.CopyRecoveryIPTx(ctx, tx, actor.ID, ids.RecoveryIPIDs); e != nil {
			t.Fatal(e)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	copyIP(tx)
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Failure after resources/releases are inserted must not expose half a copy.
	if _, err = db.Exec("CREATE TRIGGER recovery_body_fault BEFORE INSERT ON change_plan_releases FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test recovery body fault'"); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, copyErr := ip.CopyRecoveryResourcesTx(ctx, tx, actor.ID, ids)
	if copyErr == nil || counts != (service.RecoveryResourceCounts{}) {
		tx.Rollback()
		t.Fatal("late copy failure was ignored", counts, copyErr)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("DROP TRIGGER recovery_body_fault"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT COUNT(*) FROM resources WHERE project_id=?", target).Scan(&n); err != nil || n != 0 {
		t.Fatal("partial resources visible", n, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	counts, err = ip.CopyRecoveryResourcesTx(ctx, tx, actor.ID, ids)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if counts != (service.RecoveryResourceCounts{Resources: 4, Releases: 6, Imports: 1, Drafts: 3, Plans: 2}) {
		tx.Rollback()
		t.Fatal("copy lost author material", counts)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	patchDraft, err := ip.ReadPatchDraft(ctx, actor, target, ids.Branches[job.BranchID])
	if err != nil || patchDraft.Content == nil || !patchDraft.Stale || patchDraft.Content.Replacement != privatePatch.Replacement || patchDraft.Content.Group.Patches[0].BaseRevision != ids.Releases[first] || patchDraft.Content.Group.Patches[0].BaseHash != privatePatch.Group.Patches[0].BaseHash || patchDraft.Content.SelectedResourceID != ids.Resources[textID] || patchDraft.Content.OperationID == privatePatch.OperationID {
		t.Fatal("private patch was lost or silently rebased", patchDraft, err)
	}
	patchDraft.Content.Reason = "副本中继续完善"
	if _, err = ip.SavePatchDraft(ctx, actor, target, ids.Branches[job.BranchID], patchDraft); err != nil {
		t.Fatal("recovered private patch cannot be edited", err)
	}
	for _, pair := range []struct{ release, oldPlan string }{{planned, plan.ID}, {second, propagated.PlanID}} {
		var newID string
		if err = db.QueryRow("SELECT p.id FROM change_plans p JOIN change_plan_releases l ON l.plan_id=p.id WHERE p.project_id=? AND l.release_id=?", target, ids.Releases[pair.release]).Scan(&newID); err != nil {
			t.Fatal(err)
		}
		copiedPlan, readErr := ip.ReadChangePlan(ctx, actor, target, newID)
		if readErr != nil || copiedPlan.GraphSnapshot == nil || copiedPlan.ReleaseID != ids.Releases[pair.release] || copiedPlan.BranchID != ids.Branches[job.BranchID] || copiedPlan.SourceEventID != "" || copiedPlan.SemanticReview != "NOT_RUN" || len(copiedPlan.Recovery) != 1 || copiedPlan.Recovery[0].PlanID != pair.oldPlan || copiedPlan.Recovery[0].ProjectID != source {
			t.Fatal("impact plan or historical provenance lost", copiedPlan, readErr)
		}
		if pair.oldPlan == propagated.PlanID && copiedPlan.Recovery[0].SourceEventID != eventID {
			t.Fatal("publication history erased")
		}
		for _, entry := range copiedPlan.GraphSnapshot.Entries {
			if entry.Ref.BranchID != ids.Branches[job.BranchID] || entry.Ref.ID == textID || entry.Ref.ID == dependentID {
				t.Fatal("plan graph still points to original", entry)
			}
		}
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=?", target).Scan(&n); err != nil || n != 0 {
		t.Fatal("historical propagation events became executable", n, err)
	}
	read, err := ip.ReadRelease(ctx, actor, target, ids.Releases[first])
	if err != nil || read.Contents[0].Text != "正文第一稿" {
		t.Fatal("historical body lost", err)
	}
	draft, err := ip.ReadResourceDraft(ctx, actor, target, ids.Resources[textID])
	if err != nil || draft.Text != "作者尚未采用的第三稿" || draft.BaseHead != ids.Releases[second] || draft.Policy.Space != "working" {
		t.Fatal("private draft lost", draft, err)
	}
	importDraft, err := ip.ReadImportDraft(ctx, actor, target, ids.Branches[job.BranchID], ids.Imports[imported.ID])
	if err != nil || importDraft.Content.Text != "作者补充的资料意见" || importDraft.Content.OperationID == "old-import-draft" {
		t.Fatal("import draft lost", importDraft, err)
	}
	read, err = ip.ReadRelease(ctx, actor, target, ids.Releases[ai])
	if err != nil {
		t.Fatal(err)
	}
	origin := read.Manifest.Artifacts[0].Origin
	if origin.Generation == nil || origin.Generation.GenerationID != job.ID || len(origin.Recovery) != 1 || !origin.ReviewRequired || len(read.ReviewTargets) != 1 {
		t.Fatal("AI ancestry or review requirement lost", origin)
	}
	b.request("POST", "/api/projects/"+target+"/releases/"+ids.Releases[ai]+"/adopt", map[string]any{}, 503)
	// No old generation exists in the target. Editing still inherits the data-only
	// attribution and remains review-gated instead of becoming a manual release.
	edited := prepareRelease(b, target, ids.Branches[job.BranchID], "", "", "edit-recovered-ai", []map[string]any{{"resource_id": ids.Resources[aiID], "text": "恢复后继续修改的AI候选"}})
	read, err = ip.ReadRelease(ctx, actor, target, edited)
	if err != nil || read.Manifest.Artifacts[0].Origin.Generation == nil || !read.RequiresSemanticReview {
		t.Fatal("edit erased recovered AI source", err)
	}
	if err = db.QueryRow("SELECT COUNT(*) FROM generation_tasks WHERE project_id=?", target).Scan(&n); err != nil || n != 0 || transport.calls.Load() != 1 {
		t.Fatal("recovery copied or invoked old tasks", n, err)
	}
	if _, err = backup.Create(ctx, db, ip.Blobs, filepath.Join(t.TempDir(), "recovered-resources.zip")); err != nil {
		t.Fatal("recovered bodies cannot be archived", err)
	}
	newManual := prepareRelease(b, target, ids.Branches[job.BranchID], "", "", "edit-recovered-manual", []map[string]any{{"resource_id": ids.Resources[textID], "expected_head": ids.Releases[second], "text": "新副本的正文第四稿"}})
	releaseAdopt(b, target, newManual)
	releasePublish(b, target, newManual)
	var oldHead string
	if err = db.QueryRow("SELECT head_revision FROM resources WHERE id=?", textID).Scan(&oldHead); err != nil || oldHead != second {
		t.Fatal("copy edit changed original", oldHead, err)
	}
}
