package qualification_test

import (
	"context"
	"novelstudio/internal/service"
	"testing"
)

func TestFailedEvaluationStopsAlreadyPreparedChildDispatch(t *testing.T) {
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
	if _, err = f.svc.Fail(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	state, _ := f.svc.Generations.Execute(ctx, r.Steps[0].ChildID)
	if state != "BLOCKED" || p.calls.Load() != 0 {
		t.Fatal("failed parent dispatched queued provider request", state, p.calls.Load())
	}
}

func TestCancelledFinalizerCompletesUndispatchedReservationCleanup(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Advance(ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Control(ctx, f.actor, f.project, r.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.Fail(ctx, r.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	budget, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || budget.Reserved != 0 || p.calls.Load() != 0 {
		t.Fatal("durable finalizer left undispatched charge hold", budget, p.calls.Load(), err)
	}
}
