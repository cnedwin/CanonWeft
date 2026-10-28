package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
)

func TestRetentionAccountingRegistrationUnknownAndReconciliation(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "保留必要账目", "accounting-registration")
	other := createProject(b, "另一项目", "accounting-other")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	w, ctx := &service.Workspace{DB: db}, context.Background()
	budgets := &service.Budgets{Workspace: w}
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
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "PRIVATE_RECEIPT", ""); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	first, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || first.AccountingReferences < 4 || first.DeletionReady {
		t.Fatal("accounting projections absent", first, err)
	}
	var id, firstAge, raw string
	var open bool
	if err := db.QueryRow("SELECT id,CAST(first_verified_at AS CHAR),accounting_open FROM retention_references WHERE project_id=? AND owner_table='model_calls' AND field_key='accounting'", project).Scan(&id, &firstAge, &open); err != nil || !open {
		t.Fatal("unknown hold missing", open, err)
	}
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var initial retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &initial); err != nil || initial.Accounting == nil || initial.Accounting.Summary == nil || initial.Accounting.Summary.ReservedMicros != 80 || initial.Accounting.Summary.Currency != "USD" {
		t.Fatal("unknown balance changed", initial, err)
	}
	if strings.Contains(raw, "PRIVATE_") {
		t.Fatal("private source text retained")
	}
	repeat, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || repeat.NewReferences != 0 || repeat.UpdatedReferences != 0 {
		t.Fatal("replay rewrote history", repeat, err)
	}
	if _, err := budgets.Reconcile(ctx, actor.ID, call.ID, service.ReconciliationInput{OperationID: "PRIVATE_RECONCILIATION", ExpectedRevision: 1, Currency: "USD", Amount: 37, BillReference: "PRIVATE_BILL", Evidence: "PRIVATE_EVIDENCE", Reason: "PRIVATE_REASON", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || updated.UpdatedReferences != 0 {
		t.Fatal("reconciliation writer did not capture the final versions", updated, err)
	}
	var age string
	var revision int64
	if err := db.QueryRow("SELECT CAST(first_verified_at AS CHAR),revision,accounting_open FROM retention_references WHERE id=?", id).Scan(&age, &revision, &open); err != nil || age != firstAge || revision != 2 || open {
		t.Fatal("reconciliation renewed age or kept closed hold", age, revision, open, err)
	}
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=2", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var current retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &current); err != nil || current.Accounting == nil || current.Accounting.Summary.ChargedMicros != 37 || current.Accounting.Summary.AccountingRevision != 2 {
		t.Fatal("actual balance missing", current, err)
	}
	budget, err := budgets.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 37 {
		t.Fatal("registration mutated accounting", budget, err)
	}
	var foreign int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=?", other).Scan(&foreign); err != nil || foreign != 0 {
		t.Fatal("foreign project registered", foreign, err)
	}
	rows, err := db.Query("SELECT snapshot FROM retention_reference_versions WHERE project_id=?", project)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "PRIVATE_") {
			t.Fatal("private text in metadata history")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// Unknown historical projections remain open even though the current ledger
	// has been explicitly reconciled by the owner.
	if err := db.QueryRow("SELECT snapshot FROM retention_reference_versions WHERE reference_id=? AND revision=1", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var old retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &old); err != nil || !old.Reference.AccountingOpen || old.Accounting.Summary.ChargedMicros != 0 {
		t.Fatal("old balance was overwritten", old, err)
	}
}

func TestRetentionAccountingRegistrationRejectsInvalidBalance(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "坏账目拒绝登记", "accounting-bad")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	w, ctx := &service.Workspace{DB: db}, context.Background()
	budgets := &service.Budgets{Workspace: w}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := budgets.Reserve(ctx, actor, project, "accounting-bad-call", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	if _, err := db.Exec("UPDATE model_calls SET reserved_micros=-1 WHERE id=?", call.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); err == nil {
		t.Fatal("invalid balance accepted")
	}
	for _, table := range []string{"retention_source_slots", "retention_references", "retention_reference_versions", "project_lifecycle"} {
		var count int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
			t.Fatal("invalid balance partially registered", table, count, err)
		}
	}
	var reserved int64
	if err := db.QueryRow("SELECT reserved_micros FROM model_budgets LIMIT 1").Scan(&reserved); err != nil || reserved != 80 {
		t.Fatal("failure changed budget", reserved, err)
	}
	if _, err := db.Exec("UPDATE model_calls SET reserved_micros=80 WHERE id=?", call.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.AccountingReferences < 3 {
		t.Fatal("valid balance still rejected", report, err)
	}
}

func TestRetentionAccountingRegistrationWeeklyHoldSurvivesDeletion(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "周预算与删除", "accounting-week")
	branch(b, project, "正史")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	w, ctx := &service.Workspace{DB: db}, context.Background()
	budgets := &service.Budgets{Workspace: w}
	if _, err := budgets.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 1000, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := budgets.Reserve(ctx, actor, project, "PRIVATE_WEEK_CALL", 80, "USD", strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := budgets.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	// Seed the frozen qualification link after the ordinary local dispatch;
	// public budget dispatch intentionally forbids qualification execution.
	grant := attachRetentionQualificationFixture(t, db, project, actor.ID, call.ID)
	if err := budgets.Settle(ctx, call.ID, "UNKNOWN", 0, "PRIVATE_WEEK_RECEIPT", ""); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	before, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || before.AccountingReferences < 6 {
		t.Fatal(before, err)
	}
	var id, raw string
	if err := db.QueryRow("SELECT id FROM retention_references WHERE project_id=? AND owner_table='qualification_week_grants' AND storage_kind='METADATA'", project).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := w.DeleteProject(ctx, actor.ID, project, "周预算与删除", 1, "accounting-delete"); err != nil {
		t.Fatal(err)
	}
	after, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || after.DeletionReady {
		t.Fatal(after, err)
	}
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.id=?", id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &v); err != nil || v.Accounting == nil || v.Accounting.Weekly == nil || v.Accounting.Weekly.ID != grant || v.Accounting.Weekly.ReservedMicros != 80 || !v.Reference.AccountingOpen || !v.Deadline.Equal(v.Reference.FirstVerifiedAt.Add(365*24*time.Hour)) {
		t.Fatal("deleted project lost weekly hold or audit deadline", v, err)
	}
	var held int64
	if err := db.QueryRow("SELECT reserved_micros FROM qualification_week_grants WHERE id=?", grant).Scan(&held); err != nil || held != 80 {
		t.Fatal("registration settled weekly budget", held, err)
	}
	var linked int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table='qualification_calls' AND storage_kind='METADATA' AND accounting_open=TRUE", project).Scan(&linked); err != nil || linked != 1 {
		t.Fatal("qualification hold missing", linked, err)
	}
}
