package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestPublishedKnowledgeExpiryPreservesConsumersSourceAndArchive(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "发布知识清理", "published-knowledge-expiry")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "背景资料")
	body := "PRIVATE_CANARY 药师在雨夜等候故人。🌙"
	release := prepareRelease(b, project, branchID, "", "", "knowledge-release", []map[string]any{{"resource_id": rid, "text": body}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), Role: me["role"].(string), WorkspaceID: me["workspace_id"].(string)}
	knowledge := privateJobs(t, db).Knowledge
	contextDoc, err := knowledge.Search(ctx, actor, project, branchID, service.SearchInput{Query: "药师故人", Purpose: "author", Limit: 4})
	if err != nil || len(contextDoc.Evidence) != 1 {
		t.Fatal("real published retrieval", err)
	}
	indexID, chunkID := contextDoc.IndexID, contextDoc.Evidence[0].ID
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: knowledge.IP.Workspace, Blobs: store}
	index := retention.SourceKey{Table: "knowledge_indexes", Primary: map[string]string{"id": indexID}}
	chunk := retention.SourceKey{Table: "knowledge_chunks", Primary: map[string]string{"id": chunkID}}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, index, "manifest"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("live context/chunk did not protect manifest", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, chunk, "content"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("live context did not protect copied text", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, retention.SourceKey{Table: "context_packages", Primary: map[string]string{"id": contextDoc.ID}}, "payload"); err != nil {
		t.Fatal("context", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, chunk, "content"); err != nil {
		t.Fatal("chunk text", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "partial-knowledge.zip")); err != nil {
		t.Fatal("partial index archive", err)
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, index, "manifest"); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("remaining published chunk did not protect manifest", err)
	}
	for _, field := range []string{"vector", "source_hash", "resource_name"} {
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, chunk, field); err != nil {
			t.Fatal(field, err)
		}
	}
	if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, index, "manifest"); err != nil {
		t.Fatal("manifest", err)
	}
	if _, err := scanner.Scan(ctx, actor.ID, project); err != nil {
		t.Fatal("final scan", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-knowledge.zip")); err != nil {
		t.Fatal("complete index archive", err)
	}
	data, err := store.ReadObject(ctx, project, contextDoc.Evidence[0].SourceHash)
	if err != nil || string(data) != body {
		t.Fatal("index cleanup erased release source", err)
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? AND reference_id=(SELECT id FROM retention_references WHERE project_id=? AND owner_table='knowledge_indexes' AND field_key='manifest')", project, project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "missing-index-proof.zip")); err == nil {
		t.Fatal("archive trusted erased manifest without evidence")
	}
}

func TestProspectiveKnowledgeExpiryCannotResurrectAndNewReleaseWorks(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "待发布知识清理", "prospective-knowledge-expiry")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "背景资料")
	body := "少女在渡口寻找母亲留下的银色项链。🌙"
	release := prepareRelease(b, project, branchID, "", "", "prospective-release", []map[string]any{{"resource_id": rid, "text": body}})
	releaseAdopt(b, project, release)
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string), Role: me["role"].(string), WorkspaceID: me["workspace_id"].(string)}
	jobs := privateJobs(t, db)
	create := func(op, release string) service.IndexJob {
		t.Helper()
		job, err := jobs.Create(ctx, actor, project, branchID, service.IndexJobInput{ReleaseID: release, OperationID: op})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := create("first", release)
	if state, err := jobs.Execute(ctx, first.ID); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	installCompleteRetentionFixture(t, db)
	var raw string
	if err := db.QueryRow("SELECT manifest FROM knowledge_indexes WHERE id=?", first.IndexID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("knowledge_indexes", map[string]string{"id": first.IndexID})
	ref, _ := retention.ReferenceID(project, "knowledge_indexes", key, "manifest")
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'knowledge_indexes',?,'manifest','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, key); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: jobs.Knowledge.IP.Workspace, Blobs: store}
	cleanup, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || cleanup.Expiring != 5 || cleanup.RemovableObjects != 0 {
		t.Fatal("prospective index five references", cleanup, err)
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project, cleanup.ID, cleanup.PlanHash); err != nil {
		t.Fatal(err)
	}
	if state, err := scanner.AdvanceJob(ctx, cleanup.ID); err != nil || state != "DETACHING" {
		t.Fatal(state, err)
	}
	var manifest string
	if err := db.QueryRow("SELECT manifest FROM knowledge_indexes WHERE id=?", first.IndexID).Scan(&manifest); err != nil || manifest != raw {
		t.Fatal("manifest erased before chunks", err)
	}
	retry := create("old-index", release)
	if state, err := jobs.Execute(ctx, retry.ID); err != nil || state != "FAILED" {
		t.Fatal("partial index resurrected", state, err)
	}
	if _, err := jobs.Knowledge.IP.PublishRelease(ctx, actor, project, release); !errors.Is(err, domain.ErrMaterialExpired) {
		t.Fatal("partial index published", err)
	}
	finishPhysicalJob(t, scanner, cleanup.ID)
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "old-prospective.zip")); err != nil {
		t.Fatal(err)
	}
	// Same source bytes, new immutable release identity. The vector cache must
	// not reuse a retired index, and the old index must never be overwritten.
	fresh, err := jobs.Knowledge.IP.PrepareRelease(ctx, actor, project, domain.ReleaseInput{BranchID: branchID, OperationID: "new-release", Updates: []domain.ResourceUpdate{{ResourceID: rid, Text: body}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jobs.Knowledge.IP.AdoptRelease(ctx, actor, project, fresh.ID); err != nil {
		t.Fatal(err)
	}
	newIndex := create("new-index", fresh.ID)
	if newIndex.IndexID == first.IndexID {
		t.Fatal("new source identity reused erased index")
	}
	if state, err := jobs.Execute(ctx, newIndex.ID); err != nil || state != "READY" {
		t.Fatal("new index", state, err)
	}
	if _, err := jobs.Knowledge.IP.PublishRelease(ctx, actor, project, fresh.ID); err != nil {
		t.Fatal("new publication", err)
	}
	result, err := jobs.Knowledge.Search(ctx, actor, project, branchID, service.SearchInput{Query: "母亲的项链", Purpose: "author", Limit: 4})
	if err != nil || len(result.Evidence) != 1 || result.Evidence[0].Text != body || result.IndexID != newIndex.IndexID {
		t.Fatal("new source not retrieved", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "new-and-expired-knowledge.zip")); err != nil {
		t.Fatal(err)
	}
}
