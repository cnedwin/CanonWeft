package integration

import (
	"context"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/service"
	"testing"
)

func TestUnknownReconciliationIsEvidenceBoundAndNeverResends(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "账单核对", "billing")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	if _, err := svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	call, err := svc.Reserve(ctx, actor, project, "unknown", 80, "USD", "input")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	listed := b.request("GET", "/api/model-calls", nil, 200)["items"].([]any)
	if len(listed) != 1 || listed[0].(map[string]any)["currency"] != "USD" {
		t.Fatal("request accounting list incomplete", listed)
	}
	input := map[string]any{"operation_id": "invoice-1", "expected_revision": 1, "currency": "USD", "amount_micros": 45, "bill_reference": "fixture-invoice-01", "evidence": "本地测试账单：明确匹配此请求，累计收费 45 微元。", "reason": "用已匹配账单解除未知占用"}
	input["confirmed"] = true
	path := "/api/model-calls/" + call.ID + "/reconcile"
	first := b.request("POST", path, input, 200)
	same := b.request("POST", path, input, 200)
	if first["id"] != same["id"] {
		t.Fatal("duplicate adjustment")
	}
	history := b.request("GET", "/api/model-calls/"+call.ID+"/history", nil, 200)
	if len(history["adjustments"].([]any)) != 1 {
		t.Fatal("history missing", history)
	}
	budget, err := svc.Read(ctx, actor.ID)
	if err != nil || budget.Reserved != 0 || budget.Spent != 45 {
		t.Fatal(budget, err)
	}
	if err = svc.Dispatch(ctx, actor, project, call.ID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("reconciled request resent", err)
	}
	input["operation_id"] = "stale"
	input["amount_micros"] = 40
	b.request("POST", path, input, 409)
	input["operation_id"] = "invoice-correction"
	input["expected_revision"] = 2
	b.request("POST", path, input, 200)
	budget, err = svc.Read(ctx, actor.ID)
	if err != nil || budget.Spent != 40 {
		t.Fatal("correction charged total twice", budget, err)
	}
	// A late provider receipt is retained for review, never silently overwrites the bill.
	if err = svc.Settle(ctx, call.ID, "SUCCEEDED", 50, "late-fixture", "output"); !errors.Is(err, domain.ErrConflict) {
		t.Fatal(err)
	}
	budget, _ = svc.Read(ctx, actor.ID)
	if budget.Spent != 40 {
		t.Fatal("late response changed reconciliation")
	}
	var receipts int
	if err = db.QueryRow("SELECT COUNT(*) FROM model_call_receipts WHERE call_id=?", call.ID).Scan(&receipts); err != nil || receipts != 2 {
		t.Fatal("late receipt lost", receipts, err)
	}
	if _, err = svc.Configure(ctx, actor.ID, service.Budget{Currency: "CNY", Limit: 100, PerCall: 100, Revision: budget.Revision}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("historical currency changed", err)
	}
}

func TestReconciliationRequiresOwnerEvidenceAndCompletedDispatch(t *testing.T) {
	b, db := setup(t)
	project := createProject(b, "对账权限", "billing-permission")
	actor := domain.Actor{ID: b.request("GET", "/api/me", nil, 200)["id"].(string)}
	svc := &service.Budgets{Workspace: &service.Workspace{DB: db}}
	ctx := context.Background()
	_, err := svc.Configure(ctx, actor.ID, service.Budget{Currency: "USD", Limit: 100, PerCall: 100, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	call, err := svc.Reserve(ctx, actor, project, "test-evidence", 80, "USD", "input")
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Dispatch(ctx, actor, project, call.ID); err != nil {
		t.Fatal(err)
	}
	path := "/api/model-calls/" + call.ID + "/reconcile"
	input := map[string]any{"operation_id": "evidence-record", "expected_revision": 0, "currency": "USD", "amount_micros": 0, "bill_reference": "zero-fixture", "evidence": "本地测试账单确认此请求未收费，与此请求编号一致。", "reason": "确定无费用", "confirmed": true}
	b.request("POST", path, input, 409)
	if err = svc.Settle(ctx, call.ID, "UNKNOWN", 0, "", ""); err != nil {
		t.Fatal(err)
	}
	input["expected_revision"] = 1
	input["confirmed"] = false
	b.request("POST", path, input, 400)
	input["confirmed"] = true
	input["currency"] = "CNY"
	b.request("POST", path, input, 400)
	input["currency"] = "USD"
	b.request("POST", "/api/members", map[string]any{"username": "bill-reader", "password": "billing-reader-password", "role": "editor"}, 201)
	editor := &browser{t: t, handler: b.handler}
	editor.request("POST", "/api/login", map[string]any{"username": "bill-reader", "password": "billing-reader-password"}, 200)
	editor.request("GET", "/api/model-calls", nil, 403)
	editor.request("GET", "/api/model-calls/"+call.ID+"/history", nil, 403)
	editor.request("POST", path, input, 403)
	b.request("POST", path, input, 200)
	budget, _ := svc.Read(ctx, actor.ID)
	if budget.Spent != 0 || budget.Reserved != 0 || !budget.CurrencyLocked {
		t.Fatal("zero invoice or currency lock incorrect", budget)
	}
}
