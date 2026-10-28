package qualification_test

import (
	"context"
	"novelstudio/internal/service"
	"os"
	"testing"
)

func rejectStepAssociation(t *testing.T, f *fixture) {
	t.Helper()
	_, err := f.db.Exec("CREATE TRIGGER qualification_bind_failure BEFORE UPDATE ON qualification_steps FOR EACH ROW BEGIN IF NEW.child_id<>'' AND OLD.child_id='' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='tests-only rejected association'; END IF; END")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec("DROP TRIGGER IF EXISTS qualification_bind_failure") })
}

func TestEvaluationGenerationAssociationIsAtomicWithQueueAndCall(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	rejectStepAssociation(t, f)
	if _, err = f.svc.Advance(ctx, r.ID); err == nil {
		t.Fatal("association interruption not exercised")
	}
	var calls, queued int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM model_calls WHERE project_id=?", f.project).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=? AND kind='generation.start'", f.project).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || queued != 0 || p.calls.Load() != 0 {
		t.Fatal("queue/call committed without step association", calls, queued, p.calls.Load())
	}
	if _, err = f.db.Exec("DROP TRIGGER qualification_bind_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || r.Steps[0].ChildID == "" {
		t.Fatal("retry did not bind exact child", err)
	}
	if _, err = f.svc.Generations.Execute(ctx, r.Steps[0].ChildID); err != nil {
		t.Fatal(err)
	}
	// Exercise repair of a legacy commit-before-association window as well.
	if _, err = f.db.Exec("UPDATE qualification_steps SET child_id='',child_kind='',state='NOT_RUN' WHERE id=?", r.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || current.Steps[0].ChildID == "" || current.Steps[0].State != "SUCCEEDED" || current.Steps[0].Metrics.Cost != 30 || p.calls.Load() != 1 {
		t.Fatal("durable trusted call link not repaired", current.Steps[0], err)
	}
}

func TestReceiptRefreshPreservesCompletedChildWhenParentFails(t *testing.T) {
	f, p, input := modelFixture(t)
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
	child := r.Steps[0].ChildID
	if _, err = f.svc.Generations.Execute(ctx, child); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	var childState string
	if err = f.db.QueryRow("SELECT state FROM generation_tasks WHERE id=?", child).Scan(&childState); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || childState != "SUCCEEDED" || current.Steps[0].State != "SUCCEEDED" || current.Steps[0].Metrics.Cost != 30 || p.calls.Load() != 1 {
		t.Fatal("terminal parent rewrote completed child", childState, current.Steps[0], err)
	}
}

func TestReceiptCleanupReleasesIndividuallyPausedUnissuedChild(t *testing.T) {
	f, p, input := modelFixture(t)
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
	if _, err = f.svc.Generations.Control(ctx, f.actor, f.project, r.Steps[0].ChildID, "pause"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	b, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || b.Reserved != 0 || b.Spent != 0 || p.calls.Load() != 0 {
		t.Fatal("terminal cleanup kept paused unissued hold", b, err)
	}
}

func TestReceiptRecoveryNeverRegressesAlreadyCompletedChildAfterParentCancel(t *testing.T) {
	f, _, input := modelFixture(t)
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
	child := r.Steps[0].ChildID
	if _, err = f.svc.Generations.Execute(ctx, child); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if state, e := f.svc.Generations.Recover(ctx, child); e != nil || state != "SUCCEEDED" {
		t.Fatal("completed receipt regressed during bookkeeping", state, e)
	}
}

func TestReceiptRefreshUsesSettledCallBeforeGenerationFinalStateWrite(t *testing.T) {
	f, p, input := modelFixture(t)
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
	if _, err = f.db.Exec("CREATE TRIGGER generation_finish_failure BEFORE UPDATE ON generation_tasks FOR EACH ROW BEGIN IF NEW.state='SUCCEEDED' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='tests-only interrupted final state'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Execute(ctx, r.Steps[0].ChildID); err == nil {
		t.Fatal("final write interruption not exercised")
	}
	if _, err = f.db.Exec("DROP TRIGGER generation_finish_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || current.Steps[0].State != "SUCCEEDED" || current.Steps[0].Metrics.Cost != 30 || !current.Steps[0].Metrics.UsageKnown || p.calls.Load() != 1 {
		t.Fatal("settled receipt hidden behind incomplete local state", current.Steps[0], err)
	}
}

func TestEvaluationToolAssociationAtomicAndLegacyRecoveryWithActualReceipt(t *testing.T) {
	root := os.Getenv("STUDIO_ISOLATION_ROOT")
	if root == "" {
		t.Skip("real isolated runtime required")
	}
	f := isolated(t)
	f.svc.RuntimeRoot = root
	f.svc.ToolTrials = &service.ToolTrials{IP: f.svc.IP, RuntimeRoot: root}
	ctx := context.Background()
	preset, err := f.svc.InstallBaselines(ctx, f.actor, f.project, service.QualificationPresetInput{SuiteID: "shared-contract-v1", Kind: "tool"})
	if err != nil {
		t.Fatal(err)
	}
	input := service.QualificationInput{OperationID: service.NewID(), VersionID: preset.BaselineID, BaselineID: preset.BaselineID, BranchID: f.branch, SuiteID: "shared-contract-v1", MaxOutputTokens: 128}
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	rejectStepAssociation(t, f)
	if _, err = f.svc.Advance(ctx, r.ID); err == nil {
		t.Fatal("tool association interruption not exercised")
	}
	var jobs, queued int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM tool_trials WHERE project_id=?", f.project).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT COUNT(*) FROM outbox WHERE project_id=? AND kind='tool.start'", f.project).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 || queued != 0 {
		t.Fatal("tool queue committed without step association", jobs, queued)
	}
	if _, err = f.db.Exec("DROP TRIGGER qualification_bind_failure"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r, err = f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	child := r.Steps[0].ChildID
	if child == "" {
		t.Fatal("tool not atomically bound")
	}
	if state, e := f.svc.ToolTrials.Execute(ctx, child); e != nil || state != "SUCCEEDED" {
		t.Fatal(state, e)
	}
	if _, err = f.db.Exec("UPDATE qualification_steps SET child_id='',child_kind='',state='NOT_RUN' WHERE id=?", r.Steps[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.svc.Read(ctx, f.actor, f.project, r.ID)
	if err != nil || current.Steps[0].ChildID != child || current.Steps[0].State != "SUCCEEDED" || current.Steps[0].Metrics.OutputHash == "" {
		t.Fatal("actual tool receipt missing after legacy association window", current.Steps[0], err)
	}
}
