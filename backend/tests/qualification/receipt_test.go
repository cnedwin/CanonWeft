package qualification_test

import (
	"bytes"
	"context"
	"novelstudio/internal/service"
	"testing"
)

func TestTerminalCancelledEvaluationCollectsActualLateCharge(t *testing.T) {
	f, p, input := modelFixture(t)
	installQualificationSourceFixture(t, f)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	p.after = func() {
		if _, e := f.svc.Control(ctx, f.actor, f.project, r.ID, "cancel"); e != nil {
			t.Error(e)
		}
	}
	if _, err = f.svc.Generations.Execute(ctx, r.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "CANCELLED" || current.QualificationID != "" || current.Steps[0].State == "RUNNING" || current.Steps[0].Metrics.Cost != 30 || !current.Steps[0].Metrics.UsageKnown || current.Steps[0].Metrics.OutputHash == "" {
		t.Fatal("terminal parent hid actual cancelled child receipt", current.State, current.Steps[0])
	}
	if p.calls.Load() != 1 {
		t.Fatal("receipt-only work resent provider", p.calls.Load())
	}
	assertSemanticCaptured(t, f, "qualification_steps", current.Steps[0].ID, "metrics", 3)
	semanticSources(t, f)
	var call, output string
	var revision int64
	if err := f.db.QueryRow("SELECT c.id,c.output_hash,a.revision FROM model_calls c JOIN model_call_accounting a ON a.call_id=c.id JOIN generation_tasks g ON g.call_id=c.id WHERE g.id=?", current.Steps[0].ChildID).Scan(&call, &output, &revision); err != nil || output != "" {
		t.Fatal("diagnostic provenance made cancelled output adoptable", output, err)
	}
	budgets := &service.Budgets{Workspace: f.svc.IP.Workspace}
	if _, err := budgets.Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: 37, BillReference: "cancelled-late-invoice", Evidence: "Test invoice for cancelled diagnostic execution", Reason: "correct the actual charge", Confirmed: true}); err != nil {
		t.Fatal("diagnostic capture blocked financial reconciliation", err)
	}
	if err := f.svc.RefreshReceipts(ctx, current.ID); err != nil {
		t.Fatal(err)
	}
	semanticSources(t, f)
	if p.calls.Load() != 1 {
		t.Fatal("diagnostic refresh resent a cancelled request")
	}
}

func TestLaterInvoiceChangesCurrentAccountingButNeverFrozenReport(t *testing.T) {
	f, p, input := modelFixture(t)
	installQualificationSourceFixture(t, f)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	original, err := f.svc.Report(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var call string
	var revision int64
	if err = f.db.QueryRow("SELECT c.id,a.revision FROM model_calls c JOIN model_call_accounting a ON a.call_id=c.id JOIN generation_tasks g ON g.call_id=c.id WHERE g.id=?", r.Steps[0].ChildID).Scan(&call, &revision); err != nil {
		t.Fatal(err)
	}
	b := &service.Budgets{Workspace: f.svc.IP.Workspace}
	if _, err = b.Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: 71, BillReference: "later-invoice", Evidence: "Fixture invoice corrects a known previous charge", Reason: "adjust current charge only", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	report, err := f.svc.Report(ctx, f.actor, f.project, r.ID)
	if err != nil || !bytes.Equal(original, report) || current.ReportHash != r.ReportHash || current.QualificationID != r.QualificationID || current.Steps[0].Metrics.Cost != 71 || !current.Steps[0].Metrics.CostKnown || p.calls.Load() != 18 {
		t.Fatal("invoice altered report or requalified/reissued", current.Steps[0], err)
	}
	semanticSources(t, f)
	var reportVersions int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM retention_reference_versions v JOIN retention_references r ON r.id=v.reference_id WHERE r.project_id=? AND r.owner_table='qualification_runs' AND r.field_key='report_hash'`, f.project).Scan(&reportVersions); err != nil || reportVersions != 1 {
		t.Fatal("later accounting rebound the frozen report to current metrics", reportVersions, err)
	}
}

func TestUnknownTerminalInvoiceAppearsWithoutReissuingOrQualifying(t *testing.T) {
	f, p, input := modelFixture(t)
	installQualificationSourceFixture(t, f)
	ctx := context.Background()
	p.lost = true
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	var call string
	var revision int64
	if err = f.db.QueryRow("SELECT c.id,a.revision FROM model_calls c JOIN model_call_accounting a ON a.call_id=c.id WHERE c.project_id=?", f.project).Scan(&call, &revision); err != nil {
		t.Fatal(err)
	}
	b := &service.Budgets{Workspace: f.svc.IP.Workspace}
	if _, err = b.Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: 71, BillReference: "fixture-invoice", Evidence: "Local fixture invoice proving actual fee", Reason: "late invoice test", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "BLOCKED" || current.Steps[0].State != "RECONCILED" || current.Steps[0].Metrics.Cost != 71 || current.Steps[0].Metrics.UsageKnown || current.QualificationID != "" || p.calls.Load() != 1 {
		t.Fatal("late invoice missing or promoted to completed evaluation", current.State, current.Steps[0], p.calls.Load())
	}
}
