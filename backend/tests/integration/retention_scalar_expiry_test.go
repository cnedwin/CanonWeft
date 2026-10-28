package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestRetentionScalarExpiryPreservesUnknownMoneyAndHistory(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "短文本清理", "scalar-expiry")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ctx := context.Background()
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := budgets.Reserve(ctx, actor, project, "scalar-request", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	const original = "PRIVATE_PROVIDER_RECEIPT"
	if err := budgets.Settle(ctx, call.ID, "SUCCEEDED", 37, original, ""); err != nil {
		t.Fatal(err)
	}
	unresolved, err := budgets.Reserve(ctx, actor, project, "unresolved-scalar-request", 80, "USD", strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, unresolved.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, unresolved.ID, "UNKNOWN", 0, "unresolved-receipt", ""); err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	key := retention.SourceKey{Table: "model_calls", Primary: map[string]string{"id": call.ID}}
	owner, _ := retention.RowOwnerKey(key.Table, key.Primary)
	id, _ := retention.ReferenceID(project, key.Table, owner, "provider_receipt")
	hash := sha256.Sum256([]byte(original))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'model_calls',?,'provider_receipt','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, key); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	scanner := service.RetentionScanner{Workspace: &service.Workspace{DB: db}, Blobs: storage.LocalBlobs{Root: t.TempDir()}}
	scan, err := scanner.Scan(ctx, actor.ID, project)
	if err != nil || !scan.Complete {
		t.Fatal("actual complete inventory did not authorize expiry preparation", err)
	}
	revision := scan.InventoryRevision
	v := capturedVersion(t, db, project, key.Table, call.ID, "provider_receipt")
	e := retention.Expiry{ReferenceID: id, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: retention.Inline}
	proof, err := retention.ProjectClearedScalar(e, []byte(original), retention.EvidenceSummary{}, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatal("scalar cannot produce a supported proof", err, "allowed", retention.ClearScalarAllowed(e.Rule), "rule", e.Rule, "deadline", e.Deadline, "originalMatches", e.PayloadHash == hex.EncodeToString(hash[:]))
	}
	apply := func(commit bool) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, key, revision, []retention.Expiry{e}, []retention.ExpiredProjection{proof}); err != nil {
			return err
		}
		if commit {
			return tx.Commit()
		}
		return nil
	}
	// Simulate an in-flight owner under the same old observation; age alone
	// must not permit clearing a field still used by dispatch/settlement.
	if _, err := db.Exec("UPDATE model_calls SET state='DISPATCHED' WHERE id=?", call.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(true); err == nil {
		t.Fatal("active dispatch permitted scalar cleanup")
	}
	if _, err := db.Exec("UPDATE model_calls SET state='SUCCEEDED' WHERE id=?", call.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(false); err != nil {
		t.Fatal(err)
	}
	var actual string
	if err := db.QueryRow("SELECT provider_receipt FROM model_calls WHERE id=?", call.ID).Scan(&actual); err != nil || actual != original {
		t.Fatal("rollback erased receipt", actual, err)
	}
	if err := apply(true); err != nil {
		t.Fatal(err)
	}
	if err := apply(true); err != nil {
		t.Fatal("scalar retry failed", err)
	}
	if err := db.QueryRow("SELECT provider_receipt FROM model_calls WHERE id=?", call.ID).Scan(&actual); err != nil || actual != "" {
		t.Fatal("scalar stored an envelope or retained text", actual, err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.CaptureSourceValues(ctx, tx, ledger, project, key); err != nil {
		tx.Rollback()
		t.Fatal("selected scalar capture failed", err)
	}
	report, err := retention.RegisterSourceObservations(ctx, tx, ledger, project, nil)
	if err != nil || report.ExpiredReferences != 1 || report.UnresolvedReferences != 0 {
		tx.Rollback()
		t.Fatal("scalar rescan lost expiry", report, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next := capturedVersion(t, db, project, key.Table, call.ID, "provider_receipt")
	if next.Reference.State != retention.Expired || next.Reference.Revision != v.Reference.Revision+1 || !next.Reference.FirstVerifiedAt.Equal(v.Reference.FirstVerifiedAt) {
		t.Fatal("scalar capture renewed history")
	}
	var values int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_source_values WHERE source_id=?", id).Scan(&values); err != nil || values != 1 {
		t.Fatal("empty scalar observed as new original", values, err)
	}
	preview, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = retention.ScanSources(ctx, preview)
	preview.Rollback()
	if err != nil {
		t.Fatal("cleared scalar cannot be previewed", err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 80 || budget.Spent != 37 {
		t.Fatal("scalar cleanup changed money", budget, err)
	}
	for _, material := range archivedCurrentMaterials(t, db, project) {
		if material.ID == id {
			t.Fatal("archive resurrected cleared scalar")
		}
	}
	if rescanned, err := scanner.Scan(ctx, actor.ID, project); err != nil || !rescanned.Complete {
		t.Fatal("expired scalar prevented a verified complete rescan", err)
	}
	if _, err := db.Exec("UPDATE model_calls SET provider_receipt='tampered' WHERE id=?", call.ID); err != nil {
		t.Fatal(err)
	}
	if err := apply(true); err == nil {
		t.Fatal("scalar retry accepted changed value")
	}
}
