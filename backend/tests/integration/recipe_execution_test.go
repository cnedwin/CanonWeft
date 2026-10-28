package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"novelstudio/internal/domain"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	platformworker "novelstudio/internal/worker"
)

func recipeFixture(t *testing.T, transport *generationTransport) (*browser, *service.RecipeExecutions, domain.Actor, string, service.RecipeExecutionInput) {
	t.Helper()
	transport.response = strings.Replace(generationSuccess, `"content":"测试候选"`, `"content":"{\"summary\":\"明确的候选资料🌙\"}"`, 1)
	b, gen, actor, project, seed := generationFixture(t, transport)
	root := "/api/projects/" + project + "/capabilities"
	definition := capabilityDefinition("agent", "route-writer")
	definition["role"] = "writer"
	agent := b.request("POST", root, map[string]any{"operation_id": "recipe-agent", "definition": definition}, 201)
	recipe := capabilityDefinition("recipe", "route-recipe")
	recipe["dependencies"] = []any{capabilityRef(agent)}
	recipe["steps"] = []any{
		map[string]any{"key": "draft", "agent_id": agent["id"], "after": []string{}, "wait_for_confirmation": true},
		map[string]any{"key": "review", "agent_id": agent["id"], "after": []string{"draft"}, "wait_for_confirmation": false},
	}
	version := b.request("POST", root, map[string]any{"operation_id": "recipe-definition", "definition": recipe}, 201)
	input := service.RecipeExecutionInput{
		RecipeVersionID: version["id"].(string), BranchID: seed.BranchID, OperationID: "recipe-prepare", Trial: true,
		Input: json.RawMessage(`{"source":"明确的根输入"}`), Currency: "USD", BudgetLimitMicros: 100000,
		Steps: []service.RecipeStepInput{
			{Key: "draft", PriceID: seed.Frozen.Price.ID, MaxOutputTokens: 100, Input: json.RawMessage(`{"source":"写作资料专属"}`)},
			{Key: "review", PriceID: seed.Frozen.Price.ID, MaxOutputTokens: 100, Input: json.RawMessage(`{"source":""}`), Bindings: []domain.RecipeValueBinding{{TargetPointer: "/source", SourceStep: "draft", SourcePointer: "/summary"}}},
		},
	}
	return b, &service.RecipeExecutions{Generations: gen}, actor, project, input
}

func TestRecipeExecutionPersistsStagesAndVersionedConfirmation(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	db := recipes.Generations.IP.Workspace.DB
	installSourceRegistrationFixture(t, db)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil || run.State != "PREPARED" || len(run.Steps) != 2 || transport.calls.Load() != 0 {
		t.Fatal(run, err)
	}
	var captured int
	if err := db.QueryRow("SELECT COUNT(*) FROM retention_references WHERE project_id=? AND owner_table IN ('recipe_runs','recipe_steps') AND field_key='frozen'", project).Scan(&captured); err != nil || captured != 3 {
		t.Fatal("workflow preparation lacks source versions", captured, err)
	}
	again, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil || again.ID != run.ID {
		t.Fatal("duplicate preparation", err)
	}
	input.Input = json.RawMessage(`{"source":"changed"}`)
	if _, err = recipes.Prepare(ctx, actor, project, input); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("operation collision accepted", err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.Steps[0].GenerationID == "" || run.Steps[1].GenerationID != "" {
		t.Fatal(run, err)
	}
	child := run.Steps[0].GenerationID
	if state, err := recipes.Generations.Execute(ctx, child); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "WAITING_CONFIRMATION" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.Steps[0].OutputHash == "" || run.Steps[1].GenerationID != "" {
		t.Fatal(run, err)
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision-1, run.Steps[0].OutputHash); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("stale confirmation accepted", err)
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision, strings.Repeat("0", 64)); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("wrong output confirmed", err)
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision, run.Steps[0].OutputHash); err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.Steps[1].GenerationID == "" {
		t.Fatal(run, err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[1].GenerationID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if strings.Contains(transport.payload, "写作资料专属") || strings.Contains(transport.payload, "明确的根输入") || !strings.Contains(transport.payload, "明确的候选资料🌙") {
		t.Fatal("implicit context inherited or explicit predecessor missing")
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "COMPLETED" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.SpentMicros != 62 || run.HeldMicros != 0 || transport.calls.Load() != 2 {
		t.Fatal("fee root or attempts invalid", run, err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil || transport.calls.Load() != 2 {
		t.Fatal("completed recipe resent", err)
	}
	owner, err := retention.RowOwnerKey("recipe_steps", map[string]string{"id": run.Steps[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.QueryRow("SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='recipe_steps' AND r.owner_key=? AND r.field_key='input_hash'", project, owner).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var inputProof retention.ReferenceVersion
	if err := json.Unmarshal([]byte(raw), &inputProof); err != nil || len(inputProof.Reference.Sources) != 2 {
		t.Fatal("resolved step input did not pin its frozen recipe and predecessor output", inputProof, err)
	}
	if report, err := recipes.Generations.IP.Workspace.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		t.Fatal("actual workflow sources disagree with full registration", report, err)
	}
}

func TestRecipeExecutionPausedParentBlocksChildDispatchAndCancelPreservesUnknown(t *testing.T) {
	transport := &generationTransport{lost: true}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Control(ctx, actor, project, run.ID, "pause", run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "PAUSED" || transport.calls.Load() != 0 {
		t.Fatal("paused child dispatched", state, err)
	}
	run, err = recipes.Control(ctx, actor, project, run.ID, "resume", run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "UNKNOWN" {
		t.Fatal(state, err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "UNKNOWN" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.HeldMicros <= 0 {
		t.Fatal(run, err)
	}
	held := run.HeldMicros
	if _, err = recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "retry-unknown", Step: input.Steps[0]}); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("unknown stage retried", err)
	}
	run, err = recipes.Control(ctx, actor, project, run.ID, "cancel", run.Revision)
	if err != nil || run.State != "CANCELLED" || run.HeldMicros != held {
		t.Fatal("unknown hold released", run, err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "CANCELLED" || transport.calls.Load() != 1 {
		t.Fatal("unknown retried", state, err)
	}
}

func TestRecipeExecutionRetryKeepsHistoryAndChargesUnderSameRoot(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	installSourceRegistrationFixture(t, recipes.Generations.IP.Workspace.DB)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalChild := run.Steps[0].GenerationID
	if state, err := recipes.Generations.Execute(ctx, originalChild); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "WAITING_CONFIRMATION" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldRevision, oldOutput := run.Revision, run.Steps[0].OutputHash
	retry := service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "rewrite-stage", Step: input.Steps[0]}
	retry.Step.Input = json.RawMessage(`{"source":"作者修改后的资料"}`)
	run, err = recipes.Retry(ctx, actor, project, run.ID, retry)
	if err != nil || run.SpentMicros != 31 || run.ID == "" {
		t.Fatal(run, err)
	}
	again, err := recipes.Retry(ctx, actor, project, run.ID, retry)
	if err != nil || again.Revision != run.Revision {
		t.Fatal("retry response replay", err)
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", oldRevision, oldOutput); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("old confirmation reused", err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || len(run.Steps) != 4 {
		t.Fatal(run, err)
	}
	var newChild string
	for _, step := range run.Steps {
		if step.Key == "draft" && step.Active {
			newChild = step.GenerationID
			if step.Attempt != 2 {
				t.Fatal(step)
			}
		}
		if step.GenerationID == originalChild && (step.Active || step.OutputHash != oldOutput) {
			t.Fatal("history lost", step)
		}
	}
	if newChild == "" || newChild == originalChild {
		t.Fatal("attempt identity reused")
	}
	if state, err := recipes.Generations.Execute(ctx, newChild); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.SpentMicros != 62 || run.HeldMicros != 0 || transport.calls.Load() != 2 {
		t.Fatal("retry escaped root budget", run, err)
	}
}

func TestRecipeExecutionCancelDropsLateChildButSettlesUsage(t *testing.T) {
	transport := &generationTransport{started: make(chan struct{}), release: make(chan struct{})}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	installSourceRegistrationFixture(t, recipes.Generations.IP.Workspace.DB)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); done <- err }()
	select {
	case <-transport.started:
	case <-time.After(10 * time.Second):
		t.Fatal("child did not dispatch")
	}
	_, cancelErr := recipes.Control(ctx, actor, project, run.ID, "cancel", run.Revision)
	close(transport.release)
	if cancelErr != nil {
		t.Fatal(cancelErr)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "CANCELLED" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.SpentMicros != 31 || run.HeldMicros != 0 || run.Steps[0].OutputHash != "" || run.Steps[1].GenerationID != "" {
		t.Fatal("late output progressed or usage lost", run, err)
	}
}

func TestRecipeExecutionHTTPPersistsReadOnlyPreparation(t *testing.T) {
	transport := &generationTransport{}
	b, _, _, project, input := recipeFixture(t, transport)
	root := "/api/projects/" + project + "/recipe-executions"
	run := b.request("POST", root, input, 201)
	id := run["id"].(string)
	if run["state"] != "PREPARED" || transport.calls.Load() != 0 {
		t.Fatal(run)
	}
	b.request("POST", root, input, 201)
	list := b.request("GET", root+"?branch_id="+input.BranchID, nil, 200)
	if len(list["items"].([]any)) != 1 {
		t.Fatal(list)
	}
	b.request("GET", root+"/"+id, nil, 200)
	b.request("POST", root+"/"+id+"/confirm", map[string]any{"expected_revision": 1, "step_key": "draft", "output_hash": strings.Repeat("0", 64)}, 409)
	other := createProject(b, "另一作品", "another-recipe-project")
	b.request("GET", "/api/projects/"+other+"/recipe-executions/"+id, nil, 403)
	b.request("POST", root+"/"+id+"/control", map[string]any{"expected_revision": 1, "action": "cancel"}, 200)
	final := b.request("GET", root+"/"+id, nil, 200)
	if final["state"] != "CANCELLED" || transport.calls.Load() != 0 {
		t.Fatal(final)
	}
}

func TestRecipeExecutionRootLimitPreventsReservation(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	input.BudgetLimitMicros = 1
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "BLOCKED" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.Reason != "BUDGET_EXCEEDED" || run.HeldMicros != 0 || run.SpentMicros != 0 || transport.calls.Load() != 0 {
		t.Fatal(run, err)
	}
	if _, err = recipes.Generations.Start(ctx, actor, project, run.Steps[0].GenerationID); !errors.Is(err, domain.ErrConflict) {
		t.Fatal("blocked root bypassed through child", err)
	}
}

func TestRecipeExecutionPinsModelBeforeFirstChildAndRejectsReplacedKey(t *testing.T) {
	transport := &generationTransport{}
	b, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	config := run.Steps[0].Frozen.Config
	b.request("PUT", "/api/models/"+config.ID, map[string]any{"provider": "deepseek", "name": "changed model", "model": "deepseek-v4-pro", "base_url": "https://api.deepseek.com", "enabled": true, "revision": 1}, 200)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": config.ID, "connection_revision": 2, "revision": 1}, 200)
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" || transport.model != config.Model {
		t.Fatal("frozen model drifted", state, transport.model, err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision, run.Steps[0].OutputHash); err != nil {
		t.Fatal(err)
	}
	b.request("PUT", "/api/models/"+config.ID, map[string]any{"provider": "deepseek", "name": "replaced key", "model": "deepseek-v4-pro", "base_url": "https://api.deepseek.com", "enabled": true, "revision": 2, "api_key": "replaced-fixture-only-key"}, 200)
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "BLOCKED" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil || run.Steps[1].GenerationID != "" || transport.calls.Load() != 1 {
		t.Fatal("replaced credentials used", run, err)
	}
}

func TestRecipeExecutionConcurrentDeliveryCreatesOneChildAndOneReservation(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := recipes.Advance(ctx, run.ID); results <- err }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var generations, calls int
	if err = recipes.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT COUNT(*),COUNT(g.call_id) FROM recipe_steps s JOIN generation_tasks g ON g.id=s.generation_id WHERE s.run_id=?", run.ID).Scan(&generations, &calls); err != nil || generations != 1 || calls != 1 {
		t.Fatal(generations, calls, err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" || transport.calls.Load() != 1 {
		t.Fatal(state, err)
	}
}

func TestRecipeExecutionRetryKeepsPinnedModelAfterRoleBindingChanges(t *testing.T) {
	transport := &generationTransport{}
	b, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	config := run.Steps[0].Frozen.Config
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil || state != "SUCCEEDED" {
		t.Fatal(state, err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	b.request("PUT", "/api/models/"+config.ID, map[string]any{"provider": "deepseek", "name": "new default", "model": "deepseek-v4-pro", "base_url": "https://api.deepseek.com", "enabled": true, "revision": 1}, 200)
	b.request("PUT", "/api/model-bindings/writer", map[string]any{"connection_id": config.ID, "connection_revision": 2, "revision": 1}, 200)
	run, err = recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "keep-pinned-retry", Step: input.Steps[0]})
	if err != nil {
		t.Fatal("changing the default must not prevent a pinned retry", err)
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range run.Steps {
		if step.Active && step.Key == "draft" {
			if step.Frozen.Config.Revision != config.Revision {
				t.Fatal("retry model drifted")
			}
			if state, err := recipes.Generations.Execute(ctx, step.GenerationID); err != nil || state != "SUCCEEDED" || transport.model != config.Model {
				t.Fatal(state, transport.model, err)
			}
		}
	}
}

func TestRecipeExecutionTemporalRestartAndOutboxReplayPreserveConfirmation(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx, cancel := context.WithTimeout(context.Background(), 70*time.Second)
	defer cancel()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	namespace := "recipe-restart-" + service.NewID()
	start := func() *platformworker.Runtime {
		r, err := platformworker.StartRuntime(ctx, &service.Tasks{IP: recipes.Generations.IP}, os.Getenv("STUDIO_TEMPORAL"), namespace, platformworker.RuntimeServices{Generations: recipes.Generations})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	runtime := start()
	defer func() {
		if runtime != nil {
			runtime.Close()
		}
	}()
	readUntil := func(want string) service.RecipeExecution {
		for ctx.Err() == nil {
			value, err := recipes.Read(ctx, actor, project, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			if value.State == want {
				return value
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("workflow did not reach", want)
		return service.RecipeExecution{}
	}
	first := readUntil("WAITING_CONFIRMATION")
	if transport.calls.Load() != 1 || first.Steps[1].GenerationID != "" {
		t.Fatal("confirmation bypassed")
	}
	runtime.Close()
	runtime = nil
	db := recipes.Generations.IP.Workspace.DB
	// Simulate loss of outbox delivery acknowledgements after durable workflow
	// acceptance. Restarting must signal the same workflow and preserve approval.
	if _, err = db.ExecContext(ctx, "UPDATE outbox SET processed_at=NULL WHERE resource_id IN (?,?) AND kind IN ('recipe.start','generation.start')", run.ID, first.Steps[0].GenerationID); err != nil {
		t.Fatal(err)
	}
	runtime = start()
	for {
		var remaining int
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM outbox WHERE resource_id IN (?,?) AND processed_at IS NULL AND kind IN ('recipe.start','generation.start')", run.ID, first.Steps[0].GenerationID).Scan(&remaining); err != nil {
			t.Fatal(err)
		}
		if remaining == 0 {
			break
		}
		if ctx.Err() != nil {
			t.Fatal("redelivery did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	restored := readUntil("WAITING_CONFIRMATION")
	if restored.Steps[0].OutputHash != first.Steps[0].OutputHash || restored.Steps[1].GenerationID != "" || transport.calls.Load() != 1 {
		t.Fatal("restart resent or bypassed confirmation")
	}
	if _, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", restored.Revision, restored.Steps[0].OutputHash); err != nil {
		t.Fatal(err)
	}
	if err = runtime.WakeRecipe(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	completed := readUntil("COMPLETED")
	if completed.SpentMicros != 62 || completed.HeldMicros != 0 || transport.calls.Load() != 2 {
		t.Fatal("restart duplicated charges", completed.SpentMicros, completed.HeldMicros, transport.calls.Load())
	}
}
