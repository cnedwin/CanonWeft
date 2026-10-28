package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/memory"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestExpiredMemoryCandidatePreservesHeadProofAndAllowsFreshMemory(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "记忆候选生命周期", "memory-document-expiry")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "记忆来源")
	release := prepareRelease(b, project, branchID, "", "", "candidate-memory-source", []map[string]any{{"resource_id": rid, "text": "守护亲人是故事的主题。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	source := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	svc := &service.Memory{IP: ip}
	session, err := svc.SaveSession(ctx, actor, project, branchID, service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "继续写作"}})
	if err != nil {
		t.Fatal(err)
	}
	in := service.MemoryInput{OperationID: "old-memory", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Text: "PRIVATE_CANARY 待确认的记忆", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}}
	item, err := svc.Save(ctx, actor, project, branchID, in)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	for table, id := range map[string]string{"memory_items": item.ID, "memory_revisions": item.Revision} {
		var raw string
		if err := db.QueryRow("SELECT document FROM "+table+" WHERE id=?", id).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		owner, _ := retention.RowOwnerKey(table, map[string]string{"id": id})
		ref, _ := retention.ReferenceID(project, table, owner, "document")
		hash := sha256.Sum256([]byte(raw))
		if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,?,?,'document','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", ref, project, table, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", ref, hex.EncodeToString(hash[:])); err != nil {
			t.Fatal(err)
		}
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
	job, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || job.Expiring != 2 {
		t.Fatal("memory pair plan", job, err)
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project, job.ID, job.PlanHash); err != nil {
		t.Fatal(err)
	}
	if _, err := scanner.AdvanceJob(ctx, job.ID); err != nil {
		t.Fatal("first detach", err)
	}
	var state, headState string
	if err := db.QueryRow("SELECT JSON_UNQUOTE(JSON_EXTRACT(i.document,'$.state')),JSON_UNQUOTE(JSON_EXTRACT(r.document,'$.state')) FROM memory_items i JOIN memory_revisions r ON r.id=i.head_revision WHERE i.id=?", item.ID).Scan(&state, &headState); err != nil || state != "EXPIRED" || headState == "EXPIRED" {
		t.Fatal("head erased ahead of consumer", state, headState, err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "partial-memory.zip")); err != nil {
		t.Fatal("partial archive", err)
	}
	for n := 0; n < 7 && state != "COMPLETE"; n++ {
		state, err = scanner.AdvanceJob(ctx, job.ID)
		if err != nil {
			t.Fatal("finish memory pair", state, err)
		}
	}
	if state != "COMPLETE" {
		t.Fatal(state)
	}
	listed, err := svc.List(ctx, actor, project, branchID)
	if err != nil || len(listed.Items) != 0 || len(listed.Expired) != 1 || listed.Expired[0].ID != item.ID {
		t.Fatal("retired history", listed, err)
	}
	if _, err := svc.ReadRevision(ctx, actor, project, branchID, item.ID, item.Revision); err != domain.ErrMaterialExpired {
		t.Fatal("revision readable", err)
	}
	if _, err := svc.Save(ctx, actor, project, branchID, in); err != domain.ErrMaterialExpired {
		t.Fatal("old request resurrected memory", err)
	}
	if _, err := svc.Compile(ctx, actor, project, branchID, service.MemoryCompileInput{Purpose: "author", Session: session.ID, Budget: 65536, RequiredIDs: []string{item.ID}}); err == nil {
		t.Fatal("missing required memory silently omitted")
	}
	in.OperationID, in.Item.Text = "fresh-memory", "守护亲人"
	fresh, err := svc.Save(ctx, actor, project, branchID, in)
	if err != nil || fresh.ID == item.ID {
		t.Fatal("fresh candidate", fresh, err)
	}
	pack, err := svc.Compile(ctx, actor, project, branchID, service.MemoryCompileInput{Purpose: "author", Session: session.ID, Budget: 65536, RequiredIDs: []string{fresh.ID}})
	if err != nil || len(pack.Items) != 1 || pack.Items[0].Item.ID != fresh.ID {
		t.Fatal("fresh memory context", pack, err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-memory.zip")); err != nil {
		t.Fatal("actual archive", err)
	}
	if _, err := db.Exec("UPDATE memory_items SET head_revision=? WHERE id=?", fresh.Revision, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "wrong-head.zip")); err == nil {
		t.Fatal("erased memory accepted another head")
	}
}

func TestDeletedMemorySummaryExpiryHonorsCurrentAndHistoricalConsumers(t *testing.T) {
	b, db := setup(t)
	ctx := context.Background()
	project := createProject(b, "记忆候选生命周期", "memory-document-expiry")
	branchID := branch(b, project, "正史")
	rid := resource(b, project, branchID, "记忆来源")
	release := prepareRelease(b, project, branchID, "", "", "candidate-memory-source", []map[string]any{{"resource_id": rid, "text": "守护亲人是故事的主题。"}})
	releaseAdopt(b, project, release)
	releasePublish(b, project, release)
	source := b.request("GET", "/api/projects/"+project+"/resources/"+rid, nil, 200)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ip := propagationService(t, db)
	svc := &service.Memory{IP: ip}
	session, err := svc.SaveSession(ctx, actor, project, branchID, service.MemorySessionInput{OperationID: "session", Document: service.MemorySessionDocument{Title: "继续写作"}})
	if err != nil {
		t.Fatal(err)
	}
	in := service.MemoryInput{OperationID: "old-memory", Item: memory.Item{Session: session.ID, Kind: "theme", Visibility: "author", Text: "PRIVATE_CANARY 待确认的记忆", Sources: []memory.Source{{Resource: rid, Revision: release, Hash: source["hash"].(string), Start: 0, End: 4}}}}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: ip.Workspace, Blobs: store}
	// A published chapter summary is a protected root. Even after project
	// deletion makes it eligible, its retained volume must be retired first.
	chapterIn := in
	chapterIn.OperationID = "chapter-summary"
	chapterIn.Item.Kind, chapterIn.Item.Control = "summary", true
	chapterIn.Item.Summary = &memory.Summary{Level: "chapter", UnitID: service.NewID(), Title: "第一章"}
	chapter, err := svc.Save(ctx, actor, project, branchID, chapterIn)
	if err != nil {
		t.Fatal(err)
	}
	chapter, err = svc.Transition(ctx, actor, project, branchID, chapter.ID, "adopt", service.MemoryAction{OperationID: "adopt-chapter", ExpectedRevision: chapter.Revision})
	if err != nil {
		t.Fatal(err)
	}
	volumeIn := service.MemoryInput{OperationID: "volume-summary", Item: memory.Item{Session: session.ID, Kind: "summary", Visibility: "author", Text: "第一卷的主线", Control: true, Summary: &memory.Summary{Level: "volume", UnitID: service.NewID(), Title: "第一卷", Inputs: []memory.SummaryPin{{ID: chapter.ID, Revision: chapter.Revision, Hash: memory.SummaryTextHash(chapter.Text)}}}}}
	volumeDraft, err := svc.Save(ctx, actor, project, branchID, volumeIn)
	if err != nil {
		t.Fatal(err)
	}
	volume, err := svc.Transition(ctx, actor, project, branchID, volumeDraft.ID, "adopt", service.MemoryAction{OperationID: "adopt-volume", ExpectedRevision: volumeDraft.Revision})
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	var ledgerID string
	if err := db.QueryRow("SELECT ledger_id FROM `" + ledger.Schema + "`.sources WHERE source_database=DATABASE()").Scan(&ledgerID); err != nil {
		t.Fatal(err)
	}
	deleted := time.Now().UTC().Add(-180 * 24 * time.Hour).Truncate(time.Microsecond)
	if _, err := db.Exec("INSERT INTO `"+ledger.Schema+"`.events(ledger_id,kind,project_id,resource_id,actor_id,reason,created_at,clock_basis) VALUES(?,'project_deleted',?,?,?,'isolated summary expiry fixture',?,'UTC_V1')", ledgerID, project, project, actor.ID, deleted); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE projects SET deleted_at=?,revision=revision+1 WHERE id=?", deleted, project); err != nil {
		t.Fatal(err)
	}
	applyMemory := func(table, id string) error {
		t.Helper()
		scan, err := scanner.Scan(ctx, actor.ID, project)
		if err != nil {
			return err
		}
		v := capturedVersion(t, db, project, table, id, "document")
		expiry := retention.Expiry{ReferenceID: v.Reference.ID, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: v.Storage}
		now := time.Now().UTC().Truncate(time.Microsecond)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		batch, err := retention.LoadExpiryOwnerBatchTx(ctx, tx, project, expiry, []retention.Expiry{expiry}, now)
		if err != nil {
			return err
		}
		row, err := batch.Index.Record(table, map[string]string{"id": id})
		if err != nil {
			return err
		}
		summary, err := retention.TransientEvidence(row, "document")
		if err != nil {
			return err
		}
		proof, err := retention.ProjectExpired(expiry, []byte(row.Values["document"]), summary, now)
		if err != nil {
			return err
		}
		if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, batch.Source, scan.InventoryRevision, batch.Items, []retention.ExpiredProjection{proof}); err != nil {
			return err
		}
		return tx.Commit()
	}
	if err := applyMemory("memory_items", chapter.ID); err != nil {
		t.Fatal("retire chapter current", err)
	}
	if err := applyMemory("memory_revisions", chapter.Revision); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("retained summary failed to block child", err)
	}
	if err := applyMemory("memory_items", volume.ID); err != nil {
		t.Fatal("retire volume current", err)
	}
	if err := applyMemory("memory_revisions", chapter.Revision); !errors.Is(err, retention.ErrLiveExpiryConsumers) {
		t.Fatal("retained historical summary failed to block child", err)
	}
	for _, revision := range []string{volume.Revision, volumeDraft.Revision} {
		if err := applyMemory("memory_revisions", revision); err != nil {
			t.Fatal("retire volume history", err)
		}
	}
	if err := applyMemory("memory_revisions", chapter.Revision); err != nil {
		t.Fatal("child after consumers", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "deleted-summary-memory.zip")); err != nil {
		t.Fatal("deleted hierarchy archive", err)
	}
}
