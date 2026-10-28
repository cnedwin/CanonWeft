package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"sync"
	"testing"
)

func TestBudgetConcurrentReservationAndUnknownRecovery(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "预算测试", "budget-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := svc.Reserve(ctx, actor, project, "before-enable", 60, "USD", "fingerprint"); !errors.Is(err, service.ErrBudgetClosed) {
		t.Fatal("default gate open", err)
	}
	if _, err := svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan service.Call, 2)
	failures := make(chan error, 2)
	for _, op := range []string{"reserve-one", "reserve-two"} {
		wg.Add(1)
		go func(op string) {
			defer wg.Done()
			c, err := svc.Reserve(ctx, actor, project, op, 60, "USD", "fingerprint")
			if err != nil {
				failures <- err
			} else {
				results <- c
			}
		}(op)
	}
	wg.Wait()
	close(results)
	close(failures)
	if len(results) != 1 || len(failures) != 1 {
		t.Fatal("oversubscribed budget")
	}
	call := <-results
	for err := range failures {
		if !errors.Is(err, service.ErrBudgetExceeded) {
			t.Fatal(err)
		}
	}
	if err := svc.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	// A new service instance after restart cannot resend an unknown call.
	svc = &service.Budgets{Workspace: &service.Workspace{DB: db}}
	if err := svc.Dispatch(ctx, actor, project, call.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown call redispatched", err)
	}
	budget, err := svc.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 60 || budget.Spent != 0 {
		t.Fatal("unknown hold lost", budget, err)
	}
}

func TestBudgetCancelledLateReceiptAndDuplicateSettlement(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "取消测试", "cancel-project")
	me := b.request("GET", "/api/me", nil, 200)
	actor := domain.Actor{ID: me["id"].(string)}
	svc := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := svc.Reserve(ctx, actor, project, "cancel-call", 80, "USD", "input")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Cancel(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	held, _ := svc.Read(ctx, actor.ID)
	if held.Reserved != 80 {
		t.Fatal("cancellation released dispatched cost")
	}
	if err = svc.Settle(ctx, call.ID, "SUCCEEDED", 50, "provider-receipt", "output-hash"); err != nil {
		t.Fatal(err)
	}
	if err = svc.Settle(ctx, call.ID, "SUCCEEDED", 50, "provider-receipt", "output-hash"); err != nil {
		t.Fatal("duplicate settlement", err)
	}
	final, _ := svc.Read(ctx, actor.ID)
	if final.Reserved != 0 || final.Spent != 50 {
		t.Fatal("double charge or lost charge", final)
	}
	var state, hash string
	if err = db.QueryRow("SELECT state,output_hash FROM model_calls WHERE id=?", call.ID).Scan(&state, &hash); err != nil {
		t.Fatal(err)
	}
	if state != "CANCELLED" || hash != "" {
		t.Fatal("late result adopted", state, hash)
	}
	if err = svc.Settle(ctx, call.ID, "SUCCEEDED", 51, "different-receipt", "output-hash"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("receipt mutated", err)
	}
}
