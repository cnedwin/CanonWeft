package integration

import (
	"context"
	"os"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/retrieval"
	"novelstudio/internal/service"
)

func TestRetentionPublicIndexCapturesBeforePublication(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "公开索引保存来源", "public-index-capture")
	branchID := branch(b, project, "正史")
	resourceID := resource(b, project, branchID, "渡口资料")
	body := strings.Repeat("药师在渡口寻找能够辨认古老药方的人。", 3500)
	parts, err := retrieval.Split(body)
	if err != nil || len(parts) <= 128 {
		t.Fatal("fixture must cross capture page boundary", len(parts), err)
	}
	release := prepareRelease(b, project, branchID, "", "", "index-source-release", []map[string]any{{"resource_id": resourceID, "expected_head": "", "text": body}})
	releaseAdopt(b, project, release)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	jobs, ctx := privateJobs(t, db), context.Background()
	installSourceRegistrationFixture(t, db)
	create := func(operation string) service.IndexJob {
		t.Helper()
		job, err := jobs.Create(ctx, actor, project, branchID, service.IndexJobInput{ReleaseID: release, OperationID: operation})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	if _, err := db.Exec("CREATE TRIGGER reject_public_index_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='knowledge_chunks.content' AND (SELECT COUNT(*) FROM retention_references WHERE owner_table='knowledge_chunks' AND field_key='content')>128 THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected second page chunk proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_public_index_proof") })
	failed := create("public-proof-failure")
	if state, err := jobs.Execute(ctx, failed.ID); err != nil || state != "FAILED" {
		t.Fatal("unproven public index became ready", state, err)
	}
	for _, table := range []string{"knowledge_indexes", "knowledge_chunks"} {
		key := "id"
		if table == "knowledge_chunks" {
			key = "index_id"
		}
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE "+key+"=?", failed.IndexID).Scan(&count); err != nil || count != 0 {
			t.Fatal("failed proof left partial index", table, count, err)
		}
	}
	var proofs int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table IN ('knowledge_indexes','knowledge_chunks')", project).Scan(&proofs); err != nil || proofs != 0 {
		t.Fatal("later page failure left earlier proof rows", proofs, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_public_index_proof"); err != nil {
		t.Fatal(err)
	}
	retry := create("public-proof-retry")
	if state, err := jobs.Execute(ctx, retry.ID); err != nil || state != "READY" {
		t.Fatal("public index retry", state, err)
	}
	proof := capturedVersion(t, db, project, "knowledge_indexes", retry.IndexID, "manifest")
	if proof.Reference.ContentRoot || len(proof.Reference.Sources) == 0 {
		t.Fatal("prospective index must pin its original release without claiming publication", proof)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='knowledge_chunks' AND field_key='content'", project).Scan(&proofs); err != nil || proofs != len(parts) {
		t.Fatal("not every page was captured", proofs, len(parts), err)
	}
	var chunk string
	if err := db.QueryRow("SELECT id FROM knowledge_chunks WHERE index_id=? LIMIT 1", retry.IndexID).Scan(&chunk); err != nil {
		t.Fatal(err)
	}
	content := capturedVersion(t, db, project, "knowledge_chunks", chunk, "content")
	if content.Reference.ContentRoot || len(content.Reference.Sources) != 1 || content.Reference.Sources[0].ReferenceID != proof.Reference.ID {
		t.Fatal("chunk did not pin prospective manifest", content)
	}
	if report, err := jobs.Knowledge.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("public index source closure", report, err)
	}
}

func TestRetentionPublicIndexCaptureRejectsChangedChunksAndPreservesAge(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "已发布索引原文核验", "public-index-original")
	branchID := branch(b, project, "正史")
	resourceID := resource(b, project, branchID, "药方资料")
	body := "药师在渡口辨认出祖传药方的字迹。"
	release := prepareRelease(b, project, branchID, "", "", "index-original-release", []map[string]any{{"resource_id": resourceID, "expected_head": "", "text": body}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	jobs, ctx := privateJobs(t, db), context.Background()
	var indexID, chunkID string
	if err := db.QueryRow("SELECT i.id,c.id FROM knowledge_indexes i JOIN knowledge_chunks c ON c.index_id=i.id WHERE i.project_id=?", project).Scan(&indexID, &chunkID); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	capture := func() error {
		t.Helper()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
		if err := retention.CaptureKnowledgeIndex(ctx, tx, ledger, project, indexID, jobs.Knowledge.IP.Blobs.ReadObject); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := capture(); err != nil {
		t.Fatal("published index capture", err)
	}
	first := capturedVersion(t, db, project, "knowledge_chunks", chunkID, "content")
	manifest := capturedVersion(t, db, project, "knowledge_indexes", indexID, "manifest")
	if !first.Reference.ContentRoot || !manifest.Reference.ContentRoot || len(first.Reference.Sources) != 0 {
		t.Fatal("verified publication was not preserved", first, manifest)
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET content='被替换的正文' WHERE id=?", chunkID); err != nil {
		t.Fatal(err)
	}
	if err := capture(); err == nil {
		t.Fatal("changed chunk accepted as original publication")
	}
	if _, err := db.Exec("UPDATE knowledge_chunks SET content=? WHERE id=?", body, chunkID); err != nil {
		t.Fatal(err)
	}
	if err := capture(); err != nil {
		t.Fatal("replay original index", err)
	}
	after := capturedVersion(t, db, project, "knowledge_chunks", chunkID, "content")
	if after.Reference.Revision != first.Reference.Revision || !after.Reference.FirstVerifiedAt.Equal(first.Reference.FirstVerifiedAt) {
		t.Fatal("failed verification or replay rewrote original age", first, after)
	}
}
