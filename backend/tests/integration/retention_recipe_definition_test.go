package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"novelstudio/internal/authority"
	"novelstudio/internal/backup"
	"novelstudio/internal/retention"
	"novelstudio/internal/service"
	"novelstudio/internal/storage"
)

func TestDeletedRecipeAndCapabilityDefinitionsRemainArchivable(t *testing.T) {
	transport := &generationTransport{}
	_, recipes, actor, project, input := recipeFixture(t, transport)
	ctx := context.Background()
	db := recipes.Generations.IP.Workspace.DB
	run, err := recipes.Prepare(ctx, actor, project, input)
	if err != nil {
		t.Fatal(err)
	}
	installCompleteRetentionFixture(t, db)
	ledger := authority.Ledger{Schema: os.Getenv("STUDIO_AUTHORITY_SCHEMA")}
	deleteAuthorFixture(t, db, ledger, project, actor.ID)
	store, err := storage.NewLocalRetentionStore(os.Getenv("STUDIO_BLOB_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	scanner := &service.RetentionScanner{Workspace: recipes.Generations.IP.Workspace, Blobs: store}
	expire := func(table, id, field string) {
		t.Helper()
		if err := expireAuthorFixture(t, db, scanner, ledger, actor.ID, project, retention.SourceKey{Table: table, Primary: map[string]string{"id": id}}, field); err != nil {
			t.Fatal(table, field, err)
		}
	}
	for _, step := range run.Steps {
		expire("recipe_steps", step.ID, "frozen")
	}
	expire("recipe_runs", run.ID, "frozen")
	for _, step := range run.Steps {
		expire("recipe_steps", step.ID, "step_key")
	}
	for _, id := range []string{run.Frozen.Definition.ID, run.Steps[0].Frozen.Agent.ID} {
		expire("capability_versions", id, "definition")
		if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "retired-definition.zip")); err != nil {
			t.Fatal("recipe and definition archive", err)
		}
	}
	if transport.calls.Load() != 0 {
		t.Fatal("cleanup dispatched a model")
	}
	if _, err := db.Exec("DELETE FROM retention_evidence WHERE project_id=? AND reference_id IN (SELECT id FROM retention_references WHERE project_id=? AND owner_table='capability_versions' AND field_key='definition')", project, project); err != nil {
		t.Fatal(err)
	}
	if _, err := backup.Create(ctx, db, store, filepath.Join(t.TempDir(), "unproved-definition.zip")); err == nil {
		t.Fatal("unproved retired definition admitted")
	}
}
