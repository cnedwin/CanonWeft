package integration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/authority"
	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionExpiredReceiptRescanKeepsCurrentAccounting(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "过期回执保留账目", "expired-receipt")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	ctx, budgets := context.Background(), &service.Budgets{Workspace: &service.Workspace{DB: db}}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := budgets.Reserve(ctx, actor, project, "unknown-request", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "PRIVATE_PROVIDER_REFERENCE", ""); err != nil {
		t.Fatal(err)
	}
	var receiptID, raw string
	if err := db.QueryRow("SELECT id,payload FROM model_call_receipts WHERE call_id=?", call.ID).Scan(&receiptID, &raw); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := retention.ScanSources(ctx, tx)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	summary, err := snapshot.Index.FinancialSummary("model_call_receipts", map[string]string{"id": receiptID})
	tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	owner, _ := retention.RowOwnerKey("model_call_receipts", map[string]string{"id": receiptID})
	id, _ := retention.ReferenceID(project, "model_call_receipts", owner, "payload")
	hash := sha256.Sum256([]byte(raw))
	if _, err := db.Exec("INSERT INTO retention_source_slots(id,project_id,owner_table,owner_key,field_key,time_basis,first_verified_at) VALUES(?,?,'model_call_receipts',?,'payload','LEGACY_REGISTERED','2020-01-02 03:04:05.000006')", id, project, owner); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_source_values(source_id,payload_hash,is_null,first_verified_at) VALUES(?,?,FALSE,'2020-01-02 03:04:05.000006')", id, hex.EncodeToString(hash[:])); err != nil {
		t.Fatal(err)
	}
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	key := retention.SourceKey{Table: "model_call_receipts", Primary: map[string]string{"id": receiptID}}
	tx, err = db.BeginTx(ctx, nil)
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
	v := capturedVersion(t, db, project, "model_call_receipts", receiptID, "payload")
	var revision int64
	if err := db.QueryRow("SELECT inventory_revision FROM project_lifecycle WHERE project_id=?", project).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	// Isolated coordinator fixture; real complete-scan admission is still gated.
	if _, err := db.Exec("UPDATE project_lifecycle SET state='CURRENT',scanned_at=UTC_TIMESTAMP(6) WHERE project_id=?", project); err != nil {
		t.Fatal(err)
	}
	e := retention.Expiry{ReferenceID: id, Rule: v.Reference.Rule, PayloadHash: v.Reference.PayloadHash, Revision: v.Reference.Revision, Deadline: v.Deadline, Storage: retention.Inline}
	proof, err := retention.ProjectExpired(e, []byte(raw), summary, time.Now().UTC().Truncate(time.Microsecond))
	if err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := retention.ApplySourceExpiryTx(ctx, tx, ledger, project, key, revision, []retention.Expiry{e}, []retention.ExpiredProjection{proof}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	report, err := retention.RegisterSourceObservations(ctx, tx, ledger, project, nil)
	if err != nil {
		tx.Rollback()
		t.Fatal("financial source rescan failed", err)
	}
	if report.ExpiredReferences != 1 || report.UnresolvedReferences != 0 {
		tx.Rollback()
		t.Fatal("financial expiry lost", report)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 80 || budget.Spent != 0 {
		t.Fatal("expired receipt changed current money", budget, err)
	}
	history, err := budgets.History(ctx, actor.ID, call.ID)
	if err != nil {
		t.Fatal("expired receipt cannot be read", err)
	}
	encoded, _ := json.Marshal(history)
	if strings.Contains(string(encoded), "PRIVATE_PROVIDER_REFERENCE") || !strings.Contains(string(encoded), "EXPIRED") {
		t.Fatal("receipt history leaked original body or hid expiry")
	}
	adjustment := service.ReconciliationInput{OperationID: "late-bill", ExpectedRevision: 1, Currency: "USD", Amount: 37, BillReference: "bill-1", Evidence: "the later actual invoice", Reason: "resolve unknown charge", Confirmed: true}
	confirmed, err := budgets.Reconcile(ctx, actor.ID, call.ID, adjustment)
	if err != nil {
		t.Fatal("expired raw receipt blocked later accounting", err)
	}
	replayed, err := budgets.Reconcile(ctx, actor.ID, call.ID, adjustment)
	if err != nil || replayed.ID != confirmed.ID {
		t.Fatal("reconciliation replay changed outcome", err)
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 37 {
		t.Fatal("later bill did not settle exactly once", budget, err)
	}
	archivedCurrentMaterials(t, db, project)
	preview, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer preview.Rollback()
	var beforeTamper string
	if err := preview.QueryRowContext(ctx, "SELECT payload FROM model_call_receipts WHERE id=?", receiptID).Scan(&beforeTamper); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE model_call_receipts SET payload=JSON_SET(payload,'$.summary.reserved_micros',0) WHERE id=?", receiptID); err != nil {
		t.Fatal(err)
	}
	// A read-only preview must use its existing snapshot for sources, original
	// versions and independent evidence, even when a later writer changes a row.
	snapshot, err = retention.ScanSources(ctx, preview)
	if err != nil {
		t.Fatal("consistent preview of expired receipt failed", err)
	}
	if _, err := snapshot.Index.FinancialSummary("model_call_receipts", map[string]string{"id": receiptID}); err != nil {
		t.Fatal("read-only preview cannot interpret erased financial history", err)
	}
	if err := preview.Commit(); err != nil {
		t.Fatal(err)
	}
	preview, err = db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = retention.ScanSources(ctx, preview)
	preview.Rollback()
	if err == nil {
		t.Fatal("fresh read-only preview accepted altered financial envelope")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = retention.RegisterSourceObservations(ctx, tx, ledger, project, nil)
	tx.Rollback()
	if err == nil {
		t.Fatal("altered financial envelope accepted")
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = retention.CaptureSourceValues(ctx, tx, ledger, project, key)
	tx.Rollback()
	if err == nil {
		t.Fatal("selected write accepted altered financial envelope")
	}
}
