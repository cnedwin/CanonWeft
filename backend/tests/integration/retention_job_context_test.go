package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionJobExpiresContextAfterItsFrozenConsumer(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	b, generations, actor, project, first := generationFixture(t, transport)
	db, ctx := generations.IP.Workspace.DB, context.Background()
	head := propose(b, project, first.BranchID, "", "context-expiry-ip", originalSnapshot())
	publish(b, project, first.BranchID, head)
	path := "/api/projects/" + project + "/branches/" + first.BranchID + "/knowledge"
	b.request("POST", path+"/index", map[string]any{}, 200)
	pack := b.request("POST", path+"/search", map[string]any{"query": "PRIVATE_CONTEXT_TO_EXPIRE", "purpose": "writer", "perspective": "lin-1", "limit": 2}, 200)
	contextID := pack["id"].(string)
	child, err := generations.Prepare(ctx, actor, project, service.GenerationInput{BranchID: first.BranchID, ExpectedIPHead: head, Role: "writer", ContextID: contextID, PriceID: first.Frozen.Price.ID, OperationID: "context-expiry-consumer", Request: models.Request{Prompt: "基于原始上下文写作", MaxOutputTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	var original string
	if err := db.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	owner, _ := retention.RowOwnerKey("context_packages", map[string]string{"id": contextID})
	id, _ := retention.ReferenceID(project, "context_packages", owner, "payload")
	hash := sha256.Sum256([]byte(original))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'context_packages',?,'payload','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: generations.IP.Workspace, Blobs: store}
	preview, err := scanner.PlanJob(ctx, actor.ID, project, "request-v2:"+service.NewID())
	if err != nil || preview.Expiring < 3 {
		t.Fatal("context and copied generation must both expire", preview, err)
	}
	v := capturedVersion(t, db, project, "context_packages", contextID, "payload")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	live, err := retention.ContextHasLiveConsumersTx(ctx, tx, project, id, at)
	if err != nil || !live {
		tx.Rollback()
		t.Fatal("live context dependency was not detected", err)
	}
	var contentHash string
	var inventoryRevision int64
	if err = tx.QueryRow("SELECT content_hash FROM context_packages WHERE id=?", contextID).Scan(&contentHash); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&inventoryRevision); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	summary, err := retention.ContextEvidence([]byte(original), contentHash)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	expiry := retention.Expiry{ReferenceID: id, Revision: v.Reference.Revision, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Deadline: v.Deadline, Storage: retention.Inline}
	proof, err := retention.ProjectExpired(expiry, []byte(original), summary, at)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	err = retention.ApplySourceExpiryTx(ctx, tx, ledger, project, retention.SourceKey{Table: "context_packages", Primary: map[string]string{"id": contextID}}, inventoryRevision, []retention.Expiry{expiry}, []retention.ExpiredProjection{proof})
	tx.Rollback()
	if err == nil {
		t.Fatal("primitive erased context with a retained consumer")
	}
	if _, err := scanner.StartJob(ctx, actor.ID, project, preview.ID, preview.PlanHash); err != nil {
		t.Fatal(err)
	}
	// The first completed source batch must retire copied execution material
	// while the original context remains readable for source verification.
	if state, err := scanner.AdvanceJob(ctx, preview.ID); err != nil || state != "DETACHING" {
		t.Fatal("context cleanup did not process consumers", state, err)
	}
	var intermediate string
	if err := db.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&intermediate); err != nil || intermediate != original {
		t.Fatal("context erased before its live consumer", err)
	}
	if _, err := db.Exec("CREATE TRIGGER fail_context_receipt BEFORE INSERT ON retention_receipts FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='context receipt fault'"); err != nil {
		t.Fatal(err)
	}
	if state, err := scanner.AdvanceJob(ctx, preview.ID); err == nil || state != "RETRY_PENDING" {
		t.Fatal("context receipt failure was not recoverable", state, err)
	}
	if err := db.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&intermediate); err != nil || intermediate != original {
		t.Fatal("failed context receipt left partial erasure", err)
	}
	if _, err := db.Exec("DROP TRIGGER fail_context_receipt"); err != nil {
		t.Fatal(err)
	}
	scanner = &service.RetentionScanner{Workspace: generations.IP.Workspace, Blobs: store}
	finishPhysicalJob(t, scanner, preview.ID)
	var expired string
	if err := db.QueryRow("SELECT payload FROM context_packages WHERE id=?", contextID).Scan(&expired); err != nil || strings.Contains(expired, "PRIVATE_CONTEXT_TO_EXPIRE") {
		t.Fatal("context raw content was retained", err)
	}
	if _, err := retention.ParseSourceProjection([]byte(expired), "context_packages.payload"); err != nil {
		t.Fatal("context lacks typed erasure proof", err)
	}
	actualProof, _ := retention.ParseSourceProjection([]byte(expired), "context_packages.payload")
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec("UPDATE context_packages SET content_hash=? WHERE id=?", strings.Repeat("e", 64), contextID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	err = retention.ApplySourceExpiryTx(ctx, tx, ledger, project, retention.SourceKey{Table: "context_packages", Primary: map[string]string{"id": contextID}}, inventoryRevision, []retention.Expiry{expiry}, []retention.ExpiredProjection{actualProof})
	tx.Rollback()
	if err == nil {
		t.Fatal("replayed context proof ignored changed source identity")
	}
	knowledge := &service.Knowledge{IP: generations.IP}
	if _, err := knowledge.ReadContext(ctx, actor, project, first.BranchID, contextID); err != domain.ErrNotReady {
		t.Fatal("expired context became usable", err)
	}
	if _, err := generations.Start(ctx, actor, project, child.ID); err == nil {
		t.Fatal("expired copied input became executable")
	}
	if _, err := store.ReadObject(ctx, project, child.Frozen.InputHash); err == nil {
		t.Fatal("copied private context request still stored")
	}
	if _, err := store.ReadObject(ctx, project, first.Frozen.InputHash); err != nil {
		t.Fatal("unrelated original task input was removed", err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "expired-context.zip")); err != nil {
		t.Fatal("actual context expiry archive rejected", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatal("context cleanup called a model")
	}
}
