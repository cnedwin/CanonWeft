package qualification_test

import (
	"context"
	"novelstudio/internal/service"
	"testing"
	"time"
)

func TestWeeklyEvaluationDefaultClosedAndExplicitOptIn(t *testing.T) {
	f, transport, input := modelFixture(t)
	ctx := context.Background()
	week := service.QualificationWeek(time.Now())
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil {
		t.Fatal(err)
	}
	budget.Enabled = false
	if _, err = (&service.Budgets{Workspace: f.svc.IP.Workspace}).Configure(ctx, f.actor.ID, budget); err != nil {
		t.Fatal(err)
	}
	schedule, err := f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, week); err != nil {
		t.Fatal(err)
	}
	items, err := f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(items) != 0 || transport.calls.Load() != 0 {
		t.Fatal("disabled project schedule executed", len(items), err)
	}
	schedule, err = f.svc.SaveSchedule(ctx, f.actor, f.project, service.QualificationScheduleInput{Input: input, Enabled: true, Revision: schedule.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, week); err != nil {
		t.Fatal(err)
	}
	if err = f.svc.Schedule(ctx, schedule.ID, week); err != nil {
		t.Fatal(err)
	}
	items, err = f.svc.List(ctx, f.actor, f.project)
	if err != nil || len(items) != 1 || items[0].State != "BLOCKED" || transport.calls.Load() != 0 {
		t.Fatal("closed budget scheduled a paid request", items, err)
	}
}
