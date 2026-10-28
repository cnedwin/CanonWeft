package qualification_test

import (
	"context"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"strings"
	"sync"
	"testing"
	"time"
)

func largeReferenceAgent(t *testing.T, f *fixture, rootID string) string {
	t.Helper()
	ctx := context.Background()
	caps := &service.Capabilities{IP: f.svc.IP}
	root, err := caps.Read(ctx, f.actor, f.project, rootID)
	if err != nil {
		t.Fatal(err)
	}
	method, err := caps.Read(ctx, f.actor, f.project, root.Definition.Dependencies[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	packages := &service.Skills{IP: f.svc.IP}
	pkg, err := packages.Read(ctx, f.actor, f.project, method.Definition.PackageID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := packages.Edit(ctx, f.actor, f.project, pkg.ID, service.SkillEdit{OperationID: service.NewID(), ExpectedHash: pkg.Report.Hash, Changes: []service.SkillFileChange{{Path: "reference-large.txt", Text: strings.Repeat("x", 100000)}}})
	if err != nil {
		t.Fatal(err)
	}
	definition := method.Definition
	definition.PackageID = changed.ID
	definition.PackageHash = changed.Report.Hash
	m, err := caps.Save(ctx, f.actor, f.project, service.CapabilitySave{OperationID: service.NewID(), ExpectedHead: method.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	definition = root.Definition
	definition.Dependencies = []domain.CapabilityRef{{ID: m.ID, Hash: m.Hash, Kind: "skill"}}
	v, err := caps.Save(ctx, f.actor, f.project, service.CapabilitySave{OperationID: service.NewID(), ExpectedHead: root.ID, Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	return v.ID
}

func weeklyRun(t *testing.T, f *fixture, input service.QualificationInput, cap int64) (service.QualificationSchedule, service.QualificationRun) {
	t.Helper()
	ctx := context.Background()
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: cap})
	if err != nil {
		t.Fatal(err)
	}
	week := service.QualificationWeek(time.Now())
	var group sync.WaitGroup
	fail := make(chan error, 2)
	for i := 0; i < 2; i++ {
		group.Add(1)
		go func() { defer group.Done(); fail <- f.svc.Schedule(ctx, schedule.ID, week) }()
	}
	group.Wait()
	close(fail)
	for e := range fail {
		if e != nil {
			t.Fatal(e)
		}
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 1 {
		t.Fatal("weekly concurrent replay did not deduplicate", len(runs), err)
	}
	return schedule, runs[0]
}

func TestWeeklyRevisionPreservesUnknownHoldAndLateInvoice(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule, r := weeklyRun(t, f, input, q.Frozen.Estimate+1)
	p.lost = true
	r = drive(t, f, r.ID)
	var held, spent int64
	var call string
	var revision int64
	if err = f.db.QueryRow("SELECT reserved_micros,spent_micros FROM qualification_week_grants WHERE project_id=?", f.project).Scan(&held, &spent); err != nil || held <= 0 || spent != 0 {
		t.Fatal(held, spent, err)
	}
	if err = f.db.QueryRow("SELECT c.id,a.revision FROM model_calls c JOIN model_call_accounting a ON a.call_id=c.id WHERE c.project_id=?", f.project).Scan(&call, &revision); err != nil {
		t.Fatal(err)
	}
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: held, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 2 {
		t.Fatal("revised scope did not retain separate historical run", len(runs), err)
	}
	for _, run := range runs {
		if run.ID != r.ID && run.State != "BLOCKED" {
			t.Fatal("new revision reset old unknown hold", run.State)
		}
	}
	b := &service.Budgets{Workspace: f.svc.IP.Workspace}
	if _, err = b.Reconcile(ctx, f.actor.ID, call, service.ReconciliationInput{OperationID: service.NewID(), ExpectedRevision: revision, Currency: "USD", Amount: held + 123, BillReference: "weekly-late-invoice", Evidence: "Local fixture invoice with known over-reservation fee", Reason: "record late charge and overrun debt", Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT reserved_micros,spent_micros FROM qualification_week_grants WHERE project_id=?", f.project).Scan(&held, &spent); err != nil || held != 0 || spent <= schedule.BudgetLimit {
		t.Fatal("late invoice debt discarded", held, spent, err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("weekly edit or reconciliation resent request", p.calls.Load())
	}
}

func TestWeeklyUnexpectedProviderChargeIsRecordedAndStopsRemainingDispatch(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	_, r := weeklyRun(t, f, input, q.Frozen.Estimate+1)
	p.inputTokens = q.Frozen.Estimate + 200
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, r.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	if state, e := f.svc.Advance(ctx, r.ID); e != nil || state != "BLOCKED" {
		t.Fatal("overrun did not stop remaining weekly dispatch", state, e)
	}
	var held, spent int64
	if err = f.db.QueryRow("SELECT reserved_micros,spent_micros FROM qualification_week_grants WHERE project_id=?", f.project).Scan(&held, &spent); err != nil || held != 0 || spent != p.inputTokens+10 || p.calls.Load() != 1 {
		t.Fatal("actual bill was capped/discarded or resent", held, spent, p.calls.Load(), err)
	}
}

func TestQuoteIncludesCompleteCandidateAndLargerBaselinePackages(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	small, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || !small.Ready {
		t.Fatal(small.Reasons, err)
	}
	large := largeReferenceAgent(t, f, input.VersionID)
	for _, arm := range []string{"candidate", "baseline"} {
		changed := input
		if arm == "candidate" {
			changed.VersionID = large
		} else {
			changed.BaselineID = large
		}
		quote, err := f.svc.Quote(ctx, f.actor, f.project, changed)
		if err != nil {
			t.Fatal(err)
		}
		// Six repetitions carry at least100000 extra input bytes. At1micro/token,
		// the existing byte-upper-bound policy requires >=1200000 extra micros.
		if quote.Frozen.Estimate-small.Frozen.Estimate < 1200000 {
			t.Fatal("package bytes omitted from exact all-arm quote", arm, small.Frozen.Estimate, quote.Frozen.Estimate)
		}
	}
	input.VersionID = large
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: small.Frozen.Estimate + 50000})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 1 || runs[0].State != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("large package dispatched beyond quoted project cap", len(runs), p.calls.Load(), err)
	}
}

func TestWeeklyDisableIsRecheckedAtActualProviderDispatch(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: q.Frozen.Estimate + 1})
	if err != nil {
		t.Fatal(err)
	}
	week := service.QualificationWeek(time.Now())
	if err = f.svc.Schedule(ctx, schedule.ID, week); err != nil {
		t.Fatal(err)
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 1 {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, runs[0].ID); err != nil {
		t.Fatal(err)
	}
	run, err := f.svc.Read(ctx, f.actor, f.project, runs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	child := run.Steps[0].ChildID
	if child == "" {
		t.Fatal("child was not queued")
	}
	if _, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: false, BudgetLimit: q.Frozen.Estimate + 1, Revision: schedule.Revision}); err != nil {
		t.Fatal(err)
	}
	state, _ := f.svc.Generations.Execute(ctx, child)
	if state != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("withdrawn project weekly authorization still dispatched", state, p.calls.Load())
	}
	if _, err = f.svc.Generations.Recover(ctx, child); err != nil {
		t.Fatal(err)
	}
	var held, spent int64
	if err = f.db.QueryRow("SELECT reserved_micros,spent_micros FROM qualification_week_grants WHERE project_id=? AND week_key=?", f.project, week).Scan(&held, &spent); err != nil || held != 0 || spent != 0 {
		t.Fatal("unissued weekly reservation not released", held, spent, err)
	}
}

func TestWeeklyFreeToolToModelInitializesOnlyEmptyCurrencyAndKeepsExplicitCap(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	tool, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	toolInput := service.QualificationInput{OperationID: service.NewID(), VersionID: tool.BaselineID, BaselineID: tool.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", MaxOutputTokens: 128}
	schedule, first := weeklyRun(t, f, toolInput, 0)
	if first.State != "QUEUED" || first.Frozen.Weekly == nil {
		t.Fatal("free tool weekly scope unavailable", first.State)
	}
	var currency string
	if err = f.db.QueryRow("SELECT currency FROM qualification_week_grants WHERE id=?", first.Frozen.Weekly.ID).Scan(&currency); err != nil || currency != "" {
		t.Fatal(currency, err)
	}
	// Merely switching to a model with the same zero cap must stay blocked.
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: 0, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	runs, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 2 {
		t.Fatal(len(runs), err)
	}
	for _, run := range runs {
		if run.ID != first.ID && run.State != "BLOCKED" {
			t.Fatal("free-to-paid rotation acquired unapproved budget", run.State)
		}
	}
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: q.Frozen.Estimate + 1, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	runs, err = f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(runs) != 3 {
		t.Fatal("explicitly authorized paid scope failed after free tool", len(runs), err)
	}
	var model service.QualificationRun
	for _, run := range runs {
		if run.State == "QUEUED" && run.ID != first.ID {
			model = run
		}
	}
	if model.ID == "" || model.Frozen.Weekly.ID != first.Frozen.Weekly.ID || model.Frozen.Weekly.Cap != q.Frozen.Estimate+1 {
		t.Fatal("rotation reset shared ledger or changed explicit cap", model)
	}
	if _, err = f.svc.Advance(ctx, model.ID); err != nil {
		t.Fatal(err)
	}
	model, err = f.svc.Read(ctx, f.actor, f.project, model.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, model.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	var spent int64
	if err = f.db.QueryRow("SELECT currency,spent_micros FROM qualification_week_grants WHERE id=?", first.Frozen.Weekly.ID).Scan(&currency, &spent); err != nil || currency != "USD" || spent != 30 || p.calls.Load() != 1 {
		t.Fatal("wrong currency, fee or repeated send", currency, spent, p.calls.Load(), err)
	}
}
