package qualification_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRetentionRecipePinsOriginalQualifiedMethod(t *testing.T) {
	f, provider, input := modelFixture(t)
	ctx := context.Background()
	qualification, err := f.svc.Create(ctx, f.actor, f.project, input)
	if err != nil {
		t.Fatal(err)
	}
	qualification = drive(t, f, qualification.ID)
	if qualification.State != "PASSED" {
		t.Fatal("qualified recipe fixture failed")
	}
	binding, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: qualification.QualificationID, TaskKind: "shared.contract"})
	if err != nil {
		t.Fatal(err)
	}
	caps := &service.Capabilities{IP: f.svc.IP}
	agent, err := caps.Read(ctx, f.actor, f.project, input.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	definition := domain.CapabilityDefinition{Kind: "recipe", Key: "qualified-source-recipe", Name: "固定资格工作流", Description: "保留原始用途来源", Instructions: "生成候选并保留原始依据", InputSchema: agent.Definition.InputSchema, OutputSchema: agent.Definition.OutputSchema, Dependencies: []domain.CapabilityRef{{ID: agent.ID, Hash: agent.Hash, Kind: "agent"}}, Steps: []domain.RecipeStep{{Key: "draft", AgentID: agent.ID, After: []string{}, WaitForConfirmation: true}}}
	version, err := caps.Save(ctx, f.actor, f.project, service.CapabilitySave{OperationID: service.NewID(), Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	recipes := &service.RecipeExecutions{Generations: f.svc.Generations}
	installQualificationSourceFixture(t, f)
	// Existing completed qualifications are registered during upgrade before
	// a new live consumer can reference their original immutable evidence.
	semanticSources(t, f)
	run, err := recipes.Prepare(ctx, f.actor, f.project, service.RecipeExecutionInput{OperationID: service.NewID(), RecipeVersionID: version.ID, BranchID: f.branch, Currency: "USD", BudgetLimitMicros: 1000000, Input: json.RawMessage(`{"source":"工作流输入"}`), Steps: []service.RecipeStepInput{{Key: "draft", BindingID: binding.ID, TaskKind: "shared.contract", PriceID: input.PriceID, MaxOutputTokens: input.MaxOutputTokens, Input: json.RawMessage(`{"source":"原始材料"}`)}}})
	if err != nil || len(run.Steps) != 1 {
		t.Fatal("qualified recipe not prepared", err)
	}
	if _, err := f.svc.Activate(ctx, f.actor, f.project, service.BindingInput{OperationID: service.NewID(), QualificationID: qualification.QualificationID, TaskKind: "shared.contract", ExpectedBinding: binding.ID, Rollback: true}); err != nil {
		t.Fatal(err)
	}
	beforeCalls := provider.calls.Load()
	semanticSources(t, f)
	step := run.Steps[0]
	var original, originalHash string
	if err := f.db.QueryRow("SELECT frozen,frozen_hash FROM recipe_steps WHERE id=?", step.ID).Scan(&original, &originalHash); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"binding", "task", "tokens", "config"} {
		var changed service.RecipeStepFrozen
		if err := json.Unmarshal([]byte(original), &changed); err != nil {
			t.Fatal(err)
		}
		switch field {
		case "binding":
			changed.Input.BindingID = service.NewID()
		case "task":
			changed.Input.TaskKind = "shared.semantic-review"
		case "tokens":
			changed.Input.MaxOutputTokens++
		case "config":
			changed.Config.Model = "different-model"
		}
		raw, _ := json.Marshal(changed)
		if _, err := f.db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=? WHERE id=?", raw, fmt.Sprintf("%x", sha256.Sum256(raw)), step.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.IP.Workspace.RegisterRetentionSources(ctx, f.actor.ID, f.project); !errors.Is(err, domain.ErrNotReady) {
			t.Fatal("unrelated recipe qualification accepted", field, err)
		}
	}
	if _, err := f.db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=? WHERE id=?", original, originalHash, step.ID); err != nil {
		t.Fatal(err)
	}
	if report := semanticSources(t, f); report.UpdatedReferences != 0 || provider.calls.Load() != beforeCalls {
		t.Fatal("recipe source registration executed or rebound task", report)
	}
}
