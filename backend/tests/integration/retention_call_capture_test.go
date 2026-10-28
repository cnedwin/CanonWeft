package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionCallWriterUnknownReconcileAndConflictingReceipt(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "调用历史同事务", "call-writer")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	budgets := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	call, err := budgets.Reserve(ctx, actor, project, "unknown-call", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "unknown-provider-receipt", ""); err != nil {
			t.Fatal(err)
		}
	}
	var open bool
	var revision int64
	if err := db.QueryRow("SELECT accounting_open,revision FROM retention_references WHERE project_id=? AND owner_table='model_calls' AND field_key='accounting'", project).Scan(&open, &revision); err != nil || !open || revision != 3 {
		t.Fatal("unknown capture lost hold or repeated receipt rewrote state history", open, revision, err)
	}
	held, err := budgets.Read(ctx, actor.ID)
	if err != nil || held.Reserved != 80 || held.Spent != 0 {
		t.Fatal("UNKNOWN released or charged the reservation", held, err)
	}
	if _, err := budgets.Reconcile(ctx, actor.ID, call.ID, service.ReconciliationInput{OperationID: "invoice", ExpectedRevision: 1, Currency: "USD", Amount: 37, BillReference: "verified-invoice", Evidence: "actual invoice fixture evidence", Reason: "确认真实用量", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err := budgets.Settle(ctx, call.ID, "SUCCEEDED", 38, "different-receipt", ""); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("conflicting late receipt replaced reconciliation", err)
	}
	final, err := budgets.Read(ctx, actor.ID)
	if err != nil || final.Reserved != 0 || final.Spent != 37 {
		t.Fatal("source capture changed reconciled cost", final, err)
	}
	var receipts int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='model_call_receipts' AND field_key='metadata'", project).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("conflicting evidence was not preserved", receipts, err)
	}
	if err := db.QueryRow("SELECT accounting_open FROM retention_references WHERE project_id=? AND owner_table='model_calls' AND field_key='accounting'", project).Scan(&open); err != nil || open {
		t.Fatal("resolved call retains an artificial open hold", open, err)
	}
}

func TestRetentionSettlementProofFailureRollsBackAndNeverResends(t *testing.T) {
	transport := &generationTransport{response: generationSuccess}
	_, generations, actor, project, task := generationFixture(t, transport)
	db, ctx := generations.IP.Workspace.DB, context.Background()
	installSourceRegistrationFixture(t, db)
	started, err := generations.Start(ctx, actor, project, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Reject only the final proof, after the provider returned usage and the
	// settlement transaction started writing its rows and output reference.
	if _, err := db.Exec("CREATE TRIGGER reject_settlement_proof BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.accounting.summary.state'))='SUCCEEDED' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected final proof'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.Exec("DROP TRIGGER IF EXISTS reject_settlement_proof") })
	if state, err := generations.Execute(ctx, task.ID); err == nil || state != "UNKNOWN" {
		t.Fatal("partial settlement accepted", state, err)
	}
	var callState string
	var charged, reserved, spent int64
	if err := db.QueryRow("SELECT state,charged_micros FROM model_calls WHERE id=?", started.CallID).Scan(&callState, &charged); err != nil || callState != "DISPATCHED" || charged != 0 {
		t.Fatal("failed proof committed call settlement", callState, charged, err)
	}
	if err := db.QueryRow("SELECT reserved_micros,spent_micros FROM model_budgets WHERE workspace_id=?", started.WorkspaceID).Scan(&reserved, &spent); err != nil || reserved != task.Frozen.Estimate || spent != 0 {
		t.Fatal("failed proof released uncertain funds", reserved, spent, err)
	}
	var receipts, objects int
	if err := db.QueryRow("SELECT COUNT(*) FROM model_call_receipts WHERE call_id=?", started.CallID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("partial receipt committed", receipts, err)
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_objects WHERE project_id=?", project).Scan(&objects); err != nil || objects != 0 {
		t.Fatal("partial output lease committed", objects, err)
	}
	if _, err := db.Exec("DROP TRIGGER reject_settlement_proof"); err != nil {
		t.Fatal(err)
	}
	if state, err := generations.Execute(ctx, task.ID); err != nil || state != "UNKNOWN" || transport.calls.Load() != 1 {
		t.Fatal("retry repeated an uncertain external call", state, transport.calls.Load(), err)
	}
	if state, err := generations.Recover(ctx, task.ID); err != nil || state != "UNKNOWN" {
		t.Fatal("recovery did not persist the uncertain state", state, err)
	}
	var revision int64
	if err := db.QueryRow("SELECT revision FROM model_call_accounting WHERE call_id=?", started.CallID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	budgets := &service.Budgets{Workspace: generations.IP.Workspace}
	if _, err := budgets.Reconcile(ctx, actor.ID, started.CallID, service.ReconciliationInput{OperationID: "recover-proof-outage", ExpectedRevision: revision, Currency: "USD", Amount: 31, BillReference: "provider-invoice", Evidence: "verified usage after local settlement outage", Reason: "核对故障前真实用量", Confirmed: true}); err != nil {
		t.Fatal("uncertain usage cannot be reconciled", err)
	}
	final, err := budgets.Read(ctx, actor.ID)
	if err != nil || final.Spent != 31 || final.Reserved != 0 || transport.calls.Load() != 1 {
		t.Fatal("recovery changed usage or repeated dispatch", final, err)
	}
}
