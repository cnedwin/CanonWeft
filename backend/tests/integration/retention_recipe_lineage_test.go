package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func completeLineageRecipe(t *testing.T, recipes *service.RecipeExecutions, actor domain.Actor, project string, input service.RecipeExecutionInput) service.RecipeExecution {
	t.Helper()
	ctx := context.Background()
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	run, err = recipes.Start(ctx, actor, project, run.ID, run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err = recipes.Advance(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		run, err = recipes.Read(ctx, actor, project, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if state, err := recipes.Generations.Execute(ctx, run.Steps[i].GenerationID); err != nil || state != "SUCCEEDED" {
			t.Fatal(state, err)
		}
		if _, err = recipes.Advance(ctx, run.ID); err != nil {
			t.Fatal(err)
		}
		run, err = recipes.Read(ctx, actor, project, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			run, err = recipes.Confirm(ctx, actor, project, run.ID, "draft", run.Revision, run.Steps[0].OutputHash)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if run.State != "COMPLETED" {
		t.Fatal(run.State)
	}
	return run
}

func TestRetentionRecipePinsOriginalAttemptsAndResolvedInput(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run := completeLineageRecipe(t, recipes, actor, project, input)
	oldDraft, oldReview := run.Steps[0], run.Steps[1]
	// A new attempt is not evidence for the old review's input, even if it
	// uses the same agent or happens to generate the same words later.
	input.Steps[0].Input = json.RawMessage(`{"source":"重试的新输入"}`)
	if _, err := recipes.Retry(ctx, actor, project, run.ID, service.RecipeRetryInput{ExpectedRevision: run.Revision, OperationID: "lineage-retry", Step: input.Steps[0]}); err != nil {
		t.Fatal(err)
	}
	db := recipes.Generations.IP.Workspace.DB
	if _, err := backup.Create(ctx, db, recipes.Generations.IP.Blobs, filepath.Join(t.TempDir(), "recipe-source.zip")); err != nil {
		t.Fatal("real archive lost routed attempts", err)
	}
	wrongSource := oldReview.Frozen
	wrongSource.Sources = append(wrongSource.Sources[:0:0], oldReview.Frozen.Sources...)
	wrongSource.Sources[0].StepID = oldReview.ID
	wrongSource.Sources[0].OutputHash = oldReview.OutputHash
	badSource, _ := json.Marshal(wrongSource)
	if _, err := db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=SHA2(?,256) WHERE id=?", badSource, badSource, oldReview.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, recipes.Generations.IP.Blobs, filepath.Join(t.TempDir(), "invalid-recipe-source.zip")); err == nil {
		t.Fatal("archive accepted a self-referencing routed source")
	}
	validSource, _ := json.Marshal(oldReview.Frozen)
	if _, err := db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=SHA2(?,256) WHERE id=?", validSource, validSource, oldReview.ID); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UnboundSources != 0 {
		tx, beginErr := db.BeginTx(ctx, nil)
		if beginErr == nil {
			defer tx.Rollback()
			_, detail := retention.RegisterSourceObservations(ctx, tx, authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}, project, storage.LocalBlobs{Root: os.Getenv("STUDIO_BLOB_ROOT")}.ReadObject)
			t.Log("internal registration diagnostic", detail)
		}
		t.Fatal(report, err)
	}
	var snapshot string
	if err := db.QueryRow(`SELECT v.snapshot FROM retention_references r JOIN retention_reference_versions v ON v.reference_id=r.id AND v.revision=r.revision WHERE r.project_id=? AND r.owner_table='recipe_steps' AND r.field_key='input_hash' AND r.object_hash=?`, project, oldReview.InputHash).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var version retention.ReferenceVersion
	if json.Unmarshal([]byte(snapshot), &version) != nil || len(version.Reference.Sources) < 2 {
		t.Fatal("resolved input has no frozen/upstream sources", version.Reference.Sources)
	}
	key, err := retention.RowOwnerKey("recipe_steps", map[string]string{"id": oldDraft.ID})
	if err != nil {
		t.Fatal(err)
	}
	wantID, err := retention.ReferenceID(project, "recipe_steps", key, "output_hash")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, src := range version.Reference.Sources {
		if src.ReferenceID == wantID && src.Revision > 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("old predecessor attempt was not pinned", version.Reference.Sources)
	}
	var childFrozenText string
	if err := db.QueryRow("SELECT frozen FROM generation_tasks WHERE id=?", oldReview.GenerationID).Scan(&childFrozenText); err != nil {
		t.Fatal(err)
	}
	var childFrozen service.GenerationFrozen
	if json.Unmarshal([]byte(childFrozenText), &childFrozen) != nil {
		t.Fatal("bad child fixture")
	}
	originalRequestHash := childFrozen.InputHash
	requestBytes, err := recipes.Generations.IP.Blobs.ReadObject(ctx, project, originalRequestHash)
	var request models.Request
	if err != nil || json.Unmarshal(requestBytes, &request) != nil {
		t.Fatal(err)
	}
	request.Prompt = strings.ReplaceAll(request.Prompt, "明确的候选资料🌙", "未从上游获得的资料")
	changedRequest, _ := json.Marshal(request)
	if bytes.Equal(changedRequest, requestBytes) {
		t.Fatal("recipe input counterexample unchanged")
	}
	object, err := recipes.Generations.IP.Blobs.PutObject(ctx, project, bytes.NewReader(changedRequest))
	if err != nil {
		t.Fatal(err)
	}
	childFrozen.InputHash = object.Hash
	childFrozenRaw, _ := json.Marshal(childFrozen)
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", childFrozenRaw, object.Hash, oldReview.GenerationID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("request input did not match frozen predecessor but was accepted", err)
	}
	if _, err := db.Exec("UPDATE generation_tasks SET frozen=?,input_hash=? WHERE id=?", childFrozenText, originalRequestHash, oldReview.GenerationID); err != nil {
		t.Fatal(err)
	}
	var original string
	if err := db.QueryRow("SELECT frozen FROM recipe_steps WHERE id=?", oldReview.ID).Scan(&original); err != nil {
		t.Fatal(err)
	}
	var frozen map[string]any
	if err := json.Unmarshal([]byte(original), &frozen); err != nil {
		t.Fatal(err)
	}
	frozen["input"].(map[string]any)["input"] = map[string]any{"source": "", "forged": "注入"}
	changed, _ := json.Marshal(frozen)
	if _, err := db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=SHA2(?,256) WHERE id=?", changed, changed, oldReview.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.RegisterRetentionSources(ctx, actor.ID, project); !errors.Is(err, domain.ErrNotReady) {
		t.Fatal("forged resolved input accepted", err)
	}
	if _, err := db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=SHA2(?,256) WHERE id=?", original, original, oldReview.ID); err != nil {
		t.Fatal(err)
	}
	if report, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || report.UpdatedReferences != 0 || transport.calls.Load() != 2 {
		t.Fatal("registration dispatched or rewrote history", report, err)
	}
}

func TestRetentionRecipeLegacyRoutingDoesNotGuessAttempts(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	run := completeLineageRecipe(t, recipes, actor, project, input)
	db := recipes.Generations.IP.Workspace.DB
	legacy := run.Steps[1].Frozen
	legacy.Sources = nil
	raw, _ := json.Marshal(legacy)
	if _, err := db.Exec("UPDATE recipe_steps SET frozen=?,frozen_hash=SHA2(?,256) WHERE id=?", raw, raw, run.Steps[1].ID); err != nil {
		t.Fatal(err)
	}
	installSourceRegistrationFixture(t, db)
	w := &service.Workspace{DB: db}
	report, err := w.RegisterRetentionSources(ctx, actor.ID, project)
	if err != nil || report.UnboundSources == 0 {
		t.Fatal("legacy input guessed its predecessor", report, err)
	}
	if again, err := w.RegisterRetentionSources(ctx, actor.ID, project); err != nil || again.UnboundSources != report.UnboundSources || again.UpdatedReferences != 0 || transport.calls.Load() != 2 {
		t.Fatal("legacy proof was manufactured on replay", again, err)
	}
}
