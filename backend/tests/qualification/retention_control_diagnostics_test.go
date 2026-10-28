package qualification_test

import (
	"context"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionEvaluationAndScheduleDiagnostics(t *testing.T) {
	f, _, input := modelFixture(t)
	ctx := context.Background()
	run, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: 1000000})
	if err != nil {
		t.Fatal(err)
	}
	installQualificationSourceFixture(t, f)
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, BudgetLimit: 900000, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "qualification_schedules", schedule.ID, "config", 0)
	f.svc.IP.Blobs = nil
	if _, err := f.svc.Control(ctx, f.actor, f.project, run.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "qualification_runs", run.ID, "reason", 0)
	if _, err := f.db.Exec("UPDATE users SET enabled=FALSE WHERE id=?", f.actor.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Schedule(ctx, schedule.ID, service.QualificationWeek(time.Now())); err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "qualification_schedules", schedule.ID, "reason", 0)
	var reason string
	if err := f.db.QueryRow("SELECT reason FROM qualification_schedules WHERE id=?", schedule.ID).Scan(&reason); err != nil || reason == "" {
		t.Fatal("revoked schedule lacks explanation", reason, err)
	}
}

func TestRetentionSemanticCancellationDiagnosticRollsBack(t *testing.T) {
	f := isolated(t)
	ctx := context.Background()
	candidate, err := f.svc.IP.Propose(ctx, f.actor, f.project, f.branch, "", service.NewID(), domain.Snapshot{Entities: []domain.Entity{{ID: "hero", Kind: "character", Name: "林"}}})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.SemanticReviews{Generations: &service.Generations{IP: f.svc.IP}}
	run, err := svc.Prepare(ctx, f.actor, f.project, service.SemanticReviewInput{OperationID: service.NewID(), TargetKind: "ip_candidate", TargetID: candidate.ID, TargetHash: candidate.Hash, BranchID: f.branch, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	installQualificationSourceFixture(t, f)
	f.svc.IP.Blobs = nil
	if _, err := f.db.Exec("CREATE TRIGGER reject_semantic_reason BEFORE INSERT ON retention_reference_versions FOR EACH ROW BEGIN IF JSON_UNQUOTE(JSON_EXTRACT(NEW.snapshot,'$.reference.rule'))='semantic_review_runs.reason' THEN SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT='test rejected review diagnostic'; END IF; END"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = f.db.Exec("DROP TRIGGER IF EXISTS reject_semantic_reason") })
	if _, err := svc.Control(ctx, f.actor, f.project, run.ID, "cancel"); err == nil {
		t.Fatal("cancellation committed without proof")
	}
	var state string
	if err := f.db.QueryRow("SELECT state FROM semantic_review_runs WHERE id=?", run.ID).Scan(&state); err != nil || state != run.State {
		t.Fatal("partial review cancellation", state, err)
	}
	if _, err := f.db.Exec("DROP TRIGGER reject_semantic_reason"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Control(ctx, f.actor, f.project, run.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	assertSemanticCaptured(t, f, "semantic_review_runs", run.ID, "reason", 0)
}
