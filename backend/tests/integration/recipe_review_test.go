package integration

import (
	"context"
	"errors"
	"testing"

	"novelstudio/internal/domain"
	"novelstudio/internal/service"
)

func TestRecipeExecutionReviewPauseCanBeRemovedWithoutResendingFailedCalls(t *testing.T) {
	for _, lost := range []bool{false, true} {
		name := "failed"
		want := "FAILED"
		if lost {
			name = "unknown"
			want = "UNKNOWN"
		}
		t.Run(name, func(t *testing.T) {
			transport := &generationTransport{lost: lost}
			_, recipes, actor, project, input := recipeFixture(t, transport)
			transport.response = generationSuccess
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
			if _, err = recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil {
				t.Fatal(err)
			}
			if state, err := recipes.Advance(ctx, run.ID); err != nil || state != want {
				t.Fatal(state, err)
			}
			run, err = recipes.Read(ctx, actor, project, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			held := run.HeldMicros
			run, err = recipes.Control(ctx, actor, project, run.ID, "pause", run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			run, err = recipes.Control(ctx, actor, project, run.ID, "resume", run.Revision)
			if err != nil || run.Paused || run.State != want {
				t.Fatal("pause became permanent", run.State, err)
			}
			if state, err := recipes.Advance(ctx, run.ID); err != nil || state != want || transport.calls.Load() != 1 {
				t.Fatal("resume resent terminal call", state, err)
			}
			if run.HeldMicros != held {
				t.Fatal("resume changed unresolved usage")
			}
			_, err = recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "manual-after-resume", Step: input.Steps[0]})
			if lost && !errors.Is(err, domain.ErrConflict) {
				t.Fatal("uncertain call retried", err)
			}
			if !lost && err != nil {
				t.Fatal("failed result cannot be reworked", err)
			}
		})
	}
}

func TestRecipeExecutionReviewRepairsInvalidHandoffUnderOriginalBudgetRoot(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	input.Steps[1].Bindings[0].SourcePointer = "/missing"
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
	if _, err = recipes.Generations.Execute(ctx, run.Steps[0].GenerationID); err != nil {
		t.Fatal(err)
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
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "BLOCKED" {
		t.Fatal(state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	first := run.Steps[0]
	if run.Reason != "INVALID_ROUTED_INPUT" || run.Steps[1].GenerationID != "" || transport.calls.Load() != 1 {
		t.Fatal("bad handoff reached provider")
	}
	input.Steps[1].Bindings[0].SourcePointer = "/summary"
	retried, err := recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "repair-handoff", Step: input.Steps[1]})
	if err != nil {
		t.Fatal("blocked unsent stage cannot be repaired", err)
	}
	if retried.ID != run.ID || retried.SpentMicros != run.SpentMicros || retried.BudgetLimitMicros != run.BudgetLimitMicros {
		t.Fatal("repair changed original cost root")
	}
	if _, err = recipes.Advance(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Steps) != 3 || run.Steps[0].ID != first.ID || run.Steps[0].ConfirmedHash != first.ConfirmedHash {
		t.Fatal("completed predecessor was discarded")
	}
	for _, step := range run.Steps {
		if step.Active && step.Key == "review" {
			if _, err = recipes.Generations.Execute(ctx, step.GenerationID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if state, err := recipes.Advance(ctx, run.ID); err != nil || state != "COMPLETED" || transport.calls.Load() != 2 {
		t.Fatal("repair duplicated predecessor", state, err)
	}
	run, err = recipes.Read(ctx, actor, project, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	input.Steps[0].MaxOutputTokens = 2048
	var previousEvent string
	db := recipes.Generations.IP.Workspace.DB
	if err := db.QueryRow("SELECT id FROM outbox WHERE kind='recipe.retry' AND resource_id=?", run.ID).Scan(&previousEvent); err != nil {
		t.Fatal(err)
	}
	retried, err = recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "rewrite-after-repaired-handoff", Step: input.Steps[0]})
	if err != nil {
		t.Fatal("cannot redo predecessor after a repaired successor", err)
	}
	if len(retried.Steps) != 5 {
		t.Fatal("lost retry history")
	}
	if _, err := db.Exec("UPDATE outbox SET processed_at=UTC_TIMESTAMP(6) WHERE id=?", previousEvent); err != nil {
		t.Fatal(err)
	}
	var pending int
	if err := db.QueryRow("SELECT COUNT(*) FROM outbox WHERE kind='recipe.retry' AND resource_id=? AND id<>? AND processed_at IS NULL", run.ID, previousEvent).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("old delivery acknowledged new retry", err)
	}
}
