package qualification_test

import (
	"context"
	"encoding/json"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"testing"
	"time"
)

func TestConfigurationRequalificationRequiresExplicitResetAndPreservesUnknownCharge(t *testing.T) {
	f, p, input := modelFixture(t)
	ctx := context.Background()
	r, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	r = drive(t, f, r.ID)
	b1, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: r.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	input.ExpectedBinding = b1.ID
	input.MaxOutputTokens = 256
	q, err := f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || q.Ready {
		t.Fatal("different token profile falsely compared as current frozen binding", q.Ready, err)
	}
	unknown, err := f.svc.Generations.Prepare(ctx, f.actor, f.project, service.GenerationInput{BindingID: b1.ID, TaskKind: "shared.contract", BranchID: f.branch, Role: "researcher", PriceID: input.PriceID, OperationID: service.NewID(), AgentInput: json.RawMessage(`{"source":"unknown charged request"}`), Request: models.Request{Prompt: "candidate", MaxOutputTokens: 128}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Generations.Start(ctx, f.actor, f.project, unknown.ID); err != nil {
		t.Fatal(err)
	}
	p.lost = true
	if state, err := f.svc.Generations.Execute(ctx, unknown.ID); err != nil || state != "UNKNOWN" {
		t.Fatal(state, err)
	}
	p.lost = false
	before, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || before.Reserved <= 0 {
		t.Fatal("unknown hold missing", err)
	}
	connections := &service.Models{Workspace: f.svc.IP.Workspace}
	config, err := connections.Save(ctx, f.actor.ID, r.Frozen.Config.ID, service.ModelInput{Provider: "deepseek", Name: "rotated configuration", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", Enabled: true, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connections.Bind(ctx, f.actor.ID, domain.ModelBinding{Role: "researcher", ConnectionID: config.ID, ConnectionRevision: 2, Revision: 1}); err != nil {
		t.Fatal(err)
	}
	price, err := f.svc.Generations.SavePrice(ctx, f.actor.ID, service.PriceCard{ConnectionID: config.ID, ConnectionRevision: 2, Currency: "USD", Input: 1000000, Output: 1000000, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	input.PriceID = price.ID
	q, err = f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || q.Ready {
		t.Fatal("rotated model silently reused baseline configuration", q.Ready, err)
	}
	if err = f.svc.DisableBinding(ctx, f.actor, f.project, b1.ID); err != nil {
		t.Fatal(err)
	}
	bindings, err := f.svc.Bindings(ctx, f.actor, f.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		if binding.Current {
			t.Fatal("disabled current binding retained purpose head")
		}
	}
	input.ExpectedBinding = ""
	input.OperationID = service.NewID()
	q, err = f.svc.Quote(ctx, f.actor, f.project, input)
	if err != nil || !q.Ready {
		t.Fatal("explicit initial-baseline recovery unavailable", q.Reasons, err)
	}
	next, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	next = drive(t, f, next.ID)
	if next.State != "PASSED" {
		t.Fatal(next.State, next.Reason)
	}
	if _, err = f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: next.QualificationID, TaskKind: "shared.contract"}); err != nil {
		t.Fatal(err)
	}
	if state, err := f.svc.Generations.Execute(ctx, unknown.ID); err != nil || state != "UNKNOWN" {
		t.Fatal("unknown old request restarted", state, err)
	}
	after, err := (&service.Budgets{Workspace: f.svc.IP.Workspace}).Read(ctx, f.actor.ID)
	if err != nil || after.Reserved != before.Reserved || p.calls.Load() != 37 {
		t.Fatal("reset refunded or resent unknown provider request", before.Reserved, after.Reserved, p.calls.Load(), err)
	}
}
