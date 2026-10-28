package backup_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recipeRestoreTransport struct{ calls int }

func (r *recipeRestoreTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.calls++
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"id":"local-backup-receipt","choices":[{"finish_reason":"stop","message":{"content":"{\"summary\":\"需要确认的候选🌙\"}"}}],"usage":{"prompt_tokens":20,"completion_tokens":10}}`))}, nil
}

func TestRecipeRestoreKeepsAttemptsAndFeesButCannotResume(t *testing.T) {
	f := isolated(t)
	t.Setenv("STUDIO_VAULT_MASTER", filepath.Join(f.root, "master", "key.dpapi"))
	t.Setenv("STUDIO_VAULT_ROOT", filepath.Join(f.root, "vault"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	branch := service.NewID()
	execute(t, f.db, "INSERT INTO branches(id,project_id,name) VALUES(?,?,'main')", branch, f.project)
	ip := &service.IP{Workspace: &service.Workspace{DB: f.db}, Blobs: f.blobs}
	actor := domain.Actor{ID: f.user}
	connections := &service.Models{Workspace: ip.Workspace}
	config, err := connections.Save(ctx, f.user, "", service.ModelInput{Provider: "deepseek", Name: "local fixture", Model: "deepseek-flash", BaseURL: "https://api.deepseek.com", Enabled: true, APIKey: "fixture-only-not-real"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = connections.Bind(ctx, f.user, domain.ModelBinding{Role: "writer", ConnectionID: config.ID, ConnectionRevision: config.Revision}); err != nil {
		t.Fatal(err)
	}
	transport := &recipeRestoreTransport{}
	generations := &service.Generations{IP: ip, Adapter: models.New(transport)}
	price, err := generations.SavePrice(ctx, f.user, service.PriceCard{ConnectionID: config.ID, ConnectionRevision: config.Revision, Currency: "USD", Input: 1000000, Output: 2000000, Source: "https://api-docs.deepseek.com/quick_start/pricing/", ExpiresAt: time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (&service.Budgets{Workspace: ip.Workspace}).Configure(ctx, f.user, service.Budget{Currency: "USD", Limit: 1000000, PerCall: 100000, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	capabilities := &service.Capabilities{IP: ip}
	definition := domain.CapabilityDefinition{Kind: "agent", Key: "backup-writer", Name: "候选整理", Description: "测试完整候选的恢复", Instructions: "只处理指定资料，生成候选并等待作者确认。", Role: "writer", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"],"additionalProperties":false}`), OutputSchema: json.RawMessage(`{"type":"object","properties":{"summary":{"type":"string"}},"required":["summary"],"additionalProperties":false}`), Dependencies: []domain.CapabilityRef{}}
	agent, err := capabilities.Save(ctx, actor, f.project, service.CapabilitySave{OperationID: "agent", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	definition.Kind = "recipe"
	definition.Key = "backup-recipe"
	definition.Role = ""
	definition.Dependencies = []domain.CapabilityRef{{ID: agent.ID, Hash: agent.Hash, Kind: "agent"}}
	definition.Steps = []domain.RecipeStep{{Key: "draft", AgentID: agent.ID, After: []string{}, WaitForConfirmation: true}}
	recipe, err := capabilities.Save(ctx, actor, f.project, service.CapabilitySave{OperationID: "recipe", Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	svc := &service.RecipeExecutions{Generations: generations}
	stage := service.RecipeStepInput{Key: "draft", PriceID: price.ID, MaxOutputTokens: 100, Input: json.RawMessage(`{"source":"原始资料"}`)}
	run, err := svc.Prepare(ctx, actor, f.project, service.RecipeExecutionInput{RecipeVersionID: recipe.ID, BranchID: branch, OperationID: "prepare", Trial: true, Input: stage.Input, Currency: "USD", BudgetLimitMicros: 100000, Steps: []service.RecipeStepInput{stage}})
	if err != nil {
		t.Fatal(err)
	}
	run, err = svc.Start(ctx, actor, f.project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = svc.Read(ctx, actor, f.project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if state, err := svc.Advance(ctx, run.ID); err != nil || state != "WAITING_CONFIRMATION" {
		t.Fatal(state, err)
	}
	run, err = svc.Read(ctx, actor, f.project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	outputHash := run.Steps[0].OutputHash
	spent := run.SpentMicros
	stage.Input = json.RawMessage(`{"source":"重新整理资料"}`)
	run, err = svc.Retry(ctx, actor, f.project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "retry", Step: stage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = svc.Read(ctx, actor, f.project, run.ID)
	if err != nil || run.HeldMicros <= 0 {
		t.Fatal(run, err)
	}
	path := filepath.Join(f.root, "recipe.zip")
	if _, err = backup.Create(ctx, f.db, f.blobs, path); err != nil {
		t.Fatal(err)
	}
	opt := f.options(t, path, backup.ProtectedAuthority{DB: f.admin, Ledger: f.ledger})
	if _, err = backup.Restore(ctx, opt); err != nil {
		t.Fatal(err)
	}
	restored := openTarget(t, opt)
	var state string
	var disabled, cancelled bool
	if err = restored.QueryRow("SELECT state,restored_disabled,cancel_requested FROM recipe_runs WHERE id=?", run.ID).Scan(&state, &disabled, &cancelled); err != nil || state != "CANCELLED" || !disabled || !cancelled {
		t.Fatal("restore can dispatch", state, err)
	}
	var count int
	var actualSpent, held int64
	if err = restored.QueryRow("SELECT COUNT(*) FROM recipe_steps WHERE run_id=?", run.ID).Scan(&count); err != nil || count != 2 {
		t.Fatal("attempt history lost", count, err)
	}
	if err = restored.QueryRow("SELECT COALESCE(SUM(c.charged_micros),0),COALESCE(SUM(CASE WHEN c.state='UNKNOWN' THEN c.reserved_micros ELSE 0 END),0) FROM recipe_steps s JOIN generation_tasks g ON g.id=s.generation_id JOIN model_calls c ON c.id=g.call_id WHERE s.run_id=?", run.ID).Scan(&actualSpent, &held); err != nil || actualSpent != spent || held != run.HeldMicros {
		t.Fatal("money history lost", actualSpent, held, err)
	}
	for _, table := range []string{"recipe_steps", "generation_tasks", "model_connection_versions"} {
		if err = restored.QueryRow("SELECT COUNT(*) FROM " + table + " WHERE credential_ref<>''").Scan(&count); err != nil || count != 0 {
			t.Fatal("credential ref restored", table, err)
		}
	}
	if _, err = (storage.LocalBlobs{Root: opt.BlobRoot}).ReadObject(ctx, f.project, outputHash); err != nil {
		t.Fatal("old candidate lost", err)
	}
	if transport.calls != 1 {
		t.Fatal("restore dispatched a model")
	}
	var original []byte
	var frozenHash, stepID string
	if err = f.db.QueryRow("SELECT id,frozen,frozen_hash FROM recipe_steps WHERE run_id=? AND active=TRUE", run.ID).Scan(&stepID, &original, &frozenHash); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*service.RecipeStepFrozen){
		"wrong-price":   func(v *service.RecipeStepFrozen) { v.Input.PriceID = service.NewID() },
		"wrong-context": func(v *service.RecipeStepFrozen) { v.Input.ContextID = service.NewID() },
		"invalid-limit": func(v *service.RecipeStepFrozen) { v.Input.MaxOutputTokens = 0 },
		"invalid-input": func(v *service.RecipeStepFrozen) { v.Input.Input = json.RawMessage(`{"source":12}`) },
	} {
		t.Run(name, func(t *testing.T) {
			var value service.RecipeStepFrozen
			if err := json.Unmarshal(original, &value); err != nil {
				t.Fatal(err)
			}
			mutate(&value)
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			execute(t, f.db, "UPDATE recipe_steps SET frozen=?,frozen_hash=? WHERE id=?", raw, fmt.Sprintf("%x", sha256.Sum256(raw)), stepID)
			defer execute(t, f.db, "UPDATE recipe_steps SET frozen=?,frozen_hash=? WHERE id=?", original, frozenHash, stepID)
			if _, err = backup.Create(ctx, f.db, f.blobs, filepath.Join(f.root, name+".zip")); err == nil {
				t.Fatal("inconsistent frozen contract accepted")
			}
		})
	}
}
