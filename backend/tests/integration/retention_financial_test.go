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

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionFinancialEvidenceUsesActualMySQLReceiptsWithoutSettlingUnknown(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "保留账务证明", "retention-accounting")
	b.request("POST", "/api/projects/"+project+"/branches", map[string]any{"name": "正史", "operation_id": "retention-branch"}, 201)
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := budgets.Reserve(ctx, actor, project, "PRIVATE_OPERATION", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	grant := attachRetentionQualificationFixture(t, db, project, actor.ID, call.ID)
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "PRIVATE_PROVIDER_REFERENCE", ""); err != nil {
		t.Fatal(err)
	}
	var receiptID string
	var payload []byte
	if err := db.QueryRow("SELECT id,payload FROM model_call_receipts WHERE call_id=?", call.ID).Scan(&receiptID, &payload); err != nil {
		t.Fatal(err)
	}
	receipt, err := retention.ReceiptEvidence(call.ID, receiptID, payload)
	if err != nil || receipt.Outcome != "UNKNOWN" || receipt.AmountMicros != 0 {
		t.Fatal("actual receipt cannot be projected", err)
	}
	scan := func() *retention.SourceIndex {
		t.Helper()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		snapshot, err := retention.ScanSources(ctx, tx)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot.Index
	}
	summary, err := scan().FinancialSummary("model_call_receipts", map[string]string{"id": receiptID})
	if err != nil || summary.State != "UNKNOWN" || summary.ReservedMicros != 80 || summary.AccountingRevision != 1 || summary.Receipt == nil {
		t.Fatal("live ownership scanner lost receipt accounting", err)
	}
	if q := summary.QualificationAccounting; q == nil || q.HeldMicros != 80 || q.Weekly == nil || q.Weekly.ID != grant || q.Weekly.ReservedMicros != 80 {
		t.Fatal("unknown qualification hold was not retained")
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 80 || budget.Spent != 0 {
		t.Fatal("projection settled or released an unknown request", err)
	}
	input := map[string]any{"operation_id": "PRIVATE_RECONCILE_OP", "expected_revision": 1, "currency": "USD", "amount_micros": 37, "bill_reference": "PRIVATE_BILL", "evidence": "PRIVATE_EVIDENCE_SOURCE_FOR_TEST", "reason": "PRIVATE_REASON", "confirmed": true}
	adjustment := b.request("POST", "/api/model-calls/"+call.ID+"/reconcile", input, 200)
	var id, contentHash string
	var revision int64
	if err := db.QueryRow("SELECT id,revision,payload,content_hash FROM model_reconciliations WHERE id=?", adjustment["id"]).Scan(&id, &revision, &payload, &contentHash); err != nil {
		t.Fatal(err)
	}
	proof, err := retention.ReconciliationEvidence(id, call.ID, revision, contentHash, payload)
	if err != nil || proof.AmountMicros != 37 || proof.ReleasedMicros != 80 || proof.PreviousAmountMicros != 0 {
		t.Fatal("actual reconciliation cannot be projected", err)
	}
	summary, err = scan().FinancialSummary("model_reconciliations", map[string]string{"id": id})
	if err != nil || summary.State != "RECONCILED" || summary.ChargedMicros != 37 || summary.AccountingRevision != 2 || summary.Reconciliation == nil {
		t.Fatal("live ownership scanner lost reconciliation accounting", err)
	}
	if q := summary.QualificationAccounting; q == nil || q.HeldMicros != 0 || q.ChargedMicros != 37 || q.Weekly == nil || q.Weekly.ReservedMicros != 0 || q.Weekly.SpentMicros != 37 {
		t.Fatal("weekly reconciliation was not represented exactly once")
	}
	encoded, _ := json.Marshal([]any{receipt, proof})
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("source free text escaped into financial projection")
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 37 {
		t.Fatal("projection duplicated the real reconciliation", err)
	}
	// Original request/receipt identities still deduplicate late delivery.
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "PRIVATE_PROVIDER_REFERENCE", ""); err == nil {
		t.Fatal("late receipt silently replaced a reconciled bill")
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 37 {
		t.Fatal("late receipt charged twice", err)
	}
	// Stage the inactive retention tables only in this disposable database. Real
	// activation remains gated by the complete migration/backup compatibility.
	index := scan()
	ddl, err := os.ReadFile("../../migrations/retention.sql.pending")
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(string(ddl), ";") {
		if strings.TrimSpace(statement) != "" {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, item := range []struct{ table, id string }{{"model_call_receipts", receiptID}, {"model_reconciliations", id}} {
		persistRetentionFinancialFixture(t, db, index, project, item.table, item.id)
	}
	history := b.request("GET", "/api/model-calls/"+call.ID+"/history", nil, 200)
	adjustments := history["adjustments"].([]any)
	if len(adjustments) != 1 || adjustments[0].(map[string]any)["retention_state"] != "EXPIRED" {
		t.Fatal("expired reconciliation was not readable")
	}
	receipts := history["receipts"].([]any)
	if len(receipts) != 1 || receipts[0].(map[string]any)["retention_state"] != "EXPIRED" {
		t.Fatal("expired receipt was not readable")
	}
	encoded, _ = json.Marshal(history)
	if strings.Contains(string(encoded), "PRIVATE_") {
		t.Fatal("expired history exposed source text")
	}
	replay := b.request("POST", "/api/model-calls/"+call.ID+"/reconcile", input, 200)
	if replay["id"] != id || replay["retention_state"] != "EXPIRED" {
		t.Fatal("old reconciliation could not replay safely")
	}
	budget, err = budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 37 {
		t.Fatal("expired replay changed budget", err)
	}
	if _, err := db.Exec("UPDATE retention_evidence SET projection_hash=? WHERE project_id=?", strings.Repeat("0", 64), project); err != nil {
		t.Fatal(err)
	}
	if _, err := budgets.History(ctx, actor.ID, call.ID); err == nil {
		t.Fatal("expired history accepted missing or tampered proof")
	}
}

func persistRetentionFinancialFixture(t *testing.T, db *sql.DB, index *retention.SourceIndex, project, table, id string) {
	t.Helper()
	hash := func(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }
	primary := map[string]string{"id": id}
	row, err := index.Record(table, primary)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := index.FinancialSummary(table, primary)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := retention.RowOwnerKey(table, primary)
	ref, _ := retention.ReferenceID(project, table, key, "payload")
	at := time.Now().UTC().Truncate(time.Microsecond)
	expiry := retention.Expiry{ReferenceID: ref, Rule: table + ".payload", PayloadHash: hash([]byte(row.Values["payload"])), Revision: 1, Deadline: at, Storage: retention.Inline}
	projection, err := retention.ProjectExpired(expiry, []byte(row.Values["payload"]), summary, at)
	if err != nil {
		t.Fatal(err)
	}
	proof, _ := json.Marshal(projection)
	if _, err := db.Exec("INSERT INTO retention_references(id,project_id,owner_table,owner_key,field_key,class,storage_kind,payload_hash,source_created_at,time_basis,first_verified_at,deadline,state,expired_at) VALUES(?,?,?,?,'payload','RAW_RUN','INLINE',?,?,'SOURCE_UTC',?,?,'EXPIRED',?)", ref, project, table, key, expiry.PayloadHash, at.Add(-30*24*time.Hour), at, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO retention_evidence(id,project_id,reference_id,reference_revision,original_payload_hash,projection_hash,projection,expired_at) VALUES(?,?,?,1,?,?,?,?)", hash([]byte(ref)), project, ref, expiry.PayloadHash, hash(proof), proof, at); err != nil {
		t.Fatal(err)
	}
	switch table {
	case "model_call_receipts":
		_, err = db.Exec("UPDATE model_call_receipts SET payload=? WHERE id=?", proof, id)
	case "model_reconciliations":
		_, err = db.Exec("UPDATE model_reconciliations SET payload=? WHERE id=?", proof, id)
	default:
		t.Fatal("unsupported fixture owner")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// These rows establish a real accounting allocation, without executing a model.
// The assertions below exercise the production settlement/reconciliation code.
func attachRetentionQualificationFixture(t *testing.T, db *sql.DB, project, actor, call string) string {
	t.Helper()
	run, step, schedule, grant := service.NewID(), service.NewID(), service.NewID(), service.NewID()
	var branch string
	if err := db.QueryRow("SELECT id FROM branches WHERE project_id=? LIMIT 1", project).Scan(&branch); err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"INSERT INTO qualification_runs(id,project_id,branch_id,actor_id,frozen) VALUES(?,?,?,?,'{}')", []any{run, project, branch, actor}},
		{"INSERT INTO qualification_steps(id,run_id,project_id,ordinal,arm,case_key,repeat_index,metrics) VALUES(?,?,?,1,'candidate','retention',1,'{}')", []any{step, run, project}},
		{"INSERT INTO qualification_schedules(id,project_id,actor_id,config) VALUES(?,?,?,'{}')", []any{schedule, project, actor}},
		{"INSERT INTO qualification_week_grants(id,project_id,week_key,schedule_id,schedule_revision,currency,cap_micros,reserved_micros) VALUES(?,?,'2026-W43',?,1,'USD',500,80)", []any{grant, project, schedule}},
		{"INSERT INTO qualification_calls(call_id,project_id,run_id,step_id,grant_id,reserved_micros,held_micros,origin) VALUES(?,?,?,?,?,80,80,'NATIVE_30')", []any{call, project, run, step, grant}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	return grant
}
