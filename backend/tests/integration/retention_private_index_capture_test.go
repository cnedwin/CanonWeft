package integration

import (
	"context"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionPrivateIndexCapturesAtSaveAndRollsBackFailedProof(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "私人索引来源", "private-index-capture")
	branchID := branch(b, project, "正史")
	resourceID := resource(b, project, branchID, "待确认的章节")
	b.request("PUT", "/api/projects/"+project+"/resources/"+resourceID+"/draft", map[string]any{"revision": 0, "base_head": "", "text": "林舟沿着旧渡口寻找失散的朋友。"}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	jobs, ctx := privateJobs(t, db), context.Background()
	installSourceRegistrationFixture(t, db)
	create := func(operation string) service.IndexJob {
		t.Helper()
		job, err := jobs.Create(ctx, actor, project, branchID, service.IndexJobInput{Kind: "working", OperationID: operation})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := create("private-source-first")
	if state, err := jobs.Execute(ctx, first.ID); err != nil || state != "READY" {
		t.Fatal("first private index", state, err)
	}
	proof := capturedVersion(t, db, project, "working_indexes", first.IndexID, "manifest")
	if len(proof.Reference.Sources) == 0 {
		t.Fatal("private index did not pin the saved draft")
	}
	if _, err := jobs.Knowledge.IP.SaveResourceDraft(ctx, actor, project, resourceID, service.ResourceDraft{Revision: 1, Text: "林舟在雪夜找到了朋友留下的信。"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TRIGGER reject_private_index_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='working_indexes.manifest' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected private index proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_private_index_proof") })
	failed := create("private-source-failed")
	if state, err := jobs.Execute(ctx, failed.ID); err != nil || state != "FAILED" {
		t.Fatal("unproven index became ready", state, err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM working_indexes WHERE id=?", failed.IndexID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed proof left index", count, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM working_chunks WHERE index_id=?", failed.IndexID).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed proof left copied chunks", count, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_private_index_proof"); err != nil {
		t.Fatal(err)
	}
	retry := create("private-source-retry")
	if state, err := jobs.Execute(ctx, retry.ID); err != nil || state != "READY" {
		t.Fatal("private source retry", state, err)
	}
	if retry.IndexID != failed.IndexID || retry.IndexID == first.IndexID {
		t.Fatal("index fingerprint did not preserve exact draft version")
	}
	if report, err := jobs.Knowledge.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("old and current draft index lineage", report, err)
	}
	after := capturedVersion(t, db, project, "working_indexes", first.IndexID, "manifest")
	if after.Reference.Revision != proof.Reference.Revision || !after.Reference.FirstVerifiedAt.Equal(proof.Reference.FirstVerifiedAt) || after.Reference.Sources[0] != proof.Reference.Sources[0] {
		t.Fatal("new index rebound or renewed original draft evidence")
	}
}
