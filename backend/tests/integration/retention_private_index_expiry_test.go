package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

func TestRetentionPrivateIndexExpiryPreservesDraftAndAllowsNewVersion(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "工作索引过期", "private-expiry")
	branchID := branch(b, project, "正史")
	resourceID := resource(b, project, branchID, "未确认正文")
	draftPath := "/api/projects/" + project + "/resources/" + resourceID + "/draft"
	oldText := "少女在雨夜寻找失散的母亲。🌙"
	b.request("PUT", draftPath, map[string]any{"revision": 0, "base_head": "", "text": oldText}, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ctx := context.Background()
	jobs := privateJobs(t, db)
	create := func(op string) service.IndexJob {
		t.Helper()
		job, err := jobs.Create(ctx, actor, project, branchID, service.IndexJobInput{Kind: "working", OperationID: op})
		if err != nil {
			t.Fatal(err)
		}
		return job
	}
	first := create("first")
	if state, err := jobs.Execute(ctx, first.ID); err != nil || state != "READY" {
		t.Fatal(state, err)
	}
	installCompleteRetentionFixture(t, db)
	var raw string
	if err := db.QueryRow("SELECT manifest FROM working_indexes WHERE id=?", first.IndexID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey("working_indexes", map[string]string{"id": first.IndexID})
	ref, _ := retention.ReferenceID(project, "working_indexes", key, "manifest")
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'working_indexes',?,'manifest','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, key); err != nil {
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
	job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 4 || job.RemovableObjects != 0 {
		t.Fatal("index and its three chunk fields", job, err)
	}
	if _, err = scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	if state, err := scanner.AdvanceJob(ctx, job.ID); err != nil || state != "DETACHING" {
		t.Fatal(state, err)
	}
	// The manifest is still present while the first chunk batch is erased.
	var stillManifest string
	if err := db.QueryRow("SELECT manifest FROM working_indexes WHERE id=?", first.IndexID).Scan(&stillManifest); err != nil || stillManifest != raw {
		t.Fatal("parent erased before children", err)
	}
	if _, err := jobs.Knowledge.PrivateStatus(ctx, actor, project, branchID); err != domain.ErrMaterialExpired {
		t.Fatal("partially erased index ready", err)
	}
	reuse := create("cannot-resurrect")
	if state, err := jobs.Execute(ctx, reuse.ID); err != nil || state != "FAILED" {
		t.Fatal("erased identity rebuilt", state, err)
	}
	state := ""
	for i := 0; i < 8 && state != "COMPLETE"; i++ {
		state, err = scanner.AdvanceJob(ctx, job.ID)
		if err != nil {
			tx, beginErr := db.BeginTx(ctx, nil)
			if beginErr == nil {
				_, detail := retention.RegisterSourceObservations(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, store.ReadObject)
				t.Log("source diagnostic", detail)
				tx.Rollback()
			}
			t.Fatal("finish index cleanup", state, err)
		}
	}
	if state != "COMPLETE" {
		t.Fatal("cleanup incomplete", state)
	}
	var draft string
	if err := db.QueryRow("SELECT body FROM resource_drafts WHERE resource_id=? AND actor_id=?", resourceID, actor.ID).Scan(&draft); err != nil || draft != oldText {
		t.Fatal("author draft erased", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-private-index.zip")); err != nil {
		t.Fatal("expired index archive", err)
	}
	newText := "少女在旧渡口找到母亲留下的银色项链。🌙"
	if _, err := jobs.Knowledge.IP.SaveResourceDraft(ctx, actor, project, resourceID, service.ResourceDraft{Revision: 1, Text: newText}); err != nil {
		t.Fatal(err)
	}
	second := create("new-version")
	if second.IndexID == first.IndexID {
		t.Fatal("new source reused erased index identity")
	}
	if state, err := jobs.Execute(ctx, second.ID); err != nil || state != "READY" {
		t.Fatal("new index", state, err)
	}
	status, err := jobs.Knowledge.PrivateStatus(ctx, actor, project, branchID)
	if err != nil || !status.Ready || status.ID != second.IndexID {
		t.Fatal("new index unavailable", status, err)
	}
	if _, err := jobs.Knowledge.Build(ctx, actor, project, branchID); err != nil {
		t.Fatal(err)
	}
	result, err := jobs.Knowledge.Search(ctx, actor, project, branchID, service.SearchInput{Query: "母亲留下的项链", Purpose: "author", Limit: 6, IncludeWorking: true, IncludePrivateWorking: true})
	if err != nil || len(result.PrivateHits) == 0 || result.PrivateHits[0].Text != newText {
		t.Fatal("new version not retrieved", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "new-and-expired-indexes.zip")); err != nil {
		t.Fatal("coexisting index archive", err)
	}
}
