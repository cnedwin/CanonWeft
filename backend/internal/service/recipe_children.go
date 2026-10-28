package service

import (
	"context"
	"database/sql"
	"errors"
	"novelstudio/internal/domain"
)

var errRecipePaused = errors.New("parent recipe is paused")

// Only the recipe service can attach this reference; it is absent from HTTP JSON.
type recipeReference struct{ RunID, StepID string }

func recipePinnedModelTx(ctx context.Context, tx *sql.Tx, project string, input GenerationInput) (string, int64, error) {
	r, err := recipeTx(ctx, tx, project, input.recipe.RunID)
	if err != nil {
		return "", 0, err
	}
	step, err := recipeStepByID(r, input.recipe.StepID)
	if err != nil {
		return "", 0, err
	}
	if !step.Active || step.Frozen.Input.PriceID != input.PriceID {
		return "", 0, domain.ErrConflict
	}
	return step.Frozen.Config.ID, step.Frozen.Config.Revision, nil
}

func (s *Generations) validateRecipeChildTx(ctx context.Context, tx *sql.Tx, g Generation, consume bool) error {
	if err := validateRefreshContextUseTx(ctx, tx, g); err != nil {
		return err
	}
	if g.Frozen.RecipeRunID == "" {
		if g.Frozen.RecipeStepID != "" || g.Frozen.RecipeInputHash != "" {
			return domain.ErrNotReady
		}
		return nil
	}
	r, err := recipeTx(ctx, tx, g.ProjectID, g.Frozen.RecipeRunID)
	if err != nil {
		return err
	}
	step, err := recipeStepByID(r, g.Frozen.RecipeStepID)
	if err != nil {
		return err
	}
	if r.ActorID != g.ActorID || r.WorkspaceID != g.WorkspaceID || r.BranchID != g.BranchID || r.CancelRequested || r.RestoredDisabled || !step.Active || r.State == "FAILED" || r.State == "CANCELLED" {
		return domain.ErrConflict
	}
	if consume && r.Paused {
		return errRecipePaused
	}
	if consume && g.State != "SUCCEEDED" && r.State != "RUNNING" {
		return domain.ErrConflict
	}
	if consume && g.State == "SUCCEEDED" && r.State != "COMPLETED" {
		return domain.ErrConflict
	}
	if step.GenerationID != "" && step.GenerationID != g.ID {
		return domain.ErrConflict
	}
	if step.GenerationID == "" && step.State != "PREPARING" {
		return domain.ErrConflict
	}
	if g.Frozen.BaseHead != r.Frozen.BaseHead || g.Frozen.Role != step.Frozen.Agent.Definition.Role || g.Frozen.ContextID != step.Frozen.Input.ContextID || g.Frozen.Price.ID != step.Frozen.Price.ID || capabilityDigest(g.Frozen.Config) != capabilityDigest(step.Frozen.Config) || g.credentialRef != step.credentialRef || g.Frozen.RecipeInputHash == "" || g.Frozen.RecipeInputHash != step.InputHash {
		return domain.ErrConflict
	}
	trial := g.Frozen.Capability
	if trial == nil || trial.AgentVersionID != step.Frozen.Agent.ID || trial.ManifestHash != step.Frozen.ManifestHash {
		return domain.ErrConflict
	}
	if r.Frozen.Trial {
		if trial.Mode != "TRIAL" {
			return domain.ErrConflict
		}
	} else if trial.Mode != "QUALIFIED" || trial.BindingID != step.Frozen.Input.BindingID || trial.TaskKind != step.Frozen.Input.TaskKind {
		return domain.ErrConflict
	}
	return (&RecipeExecutions{Generations: s}).validateRunTx(ctx, tx, r)
}

func bindRecipeChildTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.RecipeRunID == "" {
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE recipe_steps SET generation_id=?,state='RUNNING',updated_at=UTC_TIMESTAMP(6) WHERE id=? AND run_id=? AND project_id=? AND active=TRUE AND generation_id IS NULL AND state='PREPARING'`, g.ID, g.Frozen.RecipeStepID, g.Frozen.RecipeRunID, g.ProjectID)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return domain.ErrConflict
	}
	r, err := recipeTx(ctx, tx, g.ProjectID, g.Frozen.RecipeRunID)
	if err != nil {
		return err
	}
	return updateRecipeTx(ctx, tx, &r)
}

func reserveRecipeTx(ctx context.Context, tx *sql.Tx, g Generation) error {
	if g.Frozen.RecipeRunID == "" {
		return nil
	}
	r, err := recipeTx(ctx, tx, g.ProjectID, g.Frozen.RecipeRunID)
	if err != nil {
		return err
	}
	if err = recipeFeesTx(ctx, tx, &r); err != nil {
		return err
	}
	if g.Frozen.Price.Currency != r.Currency || g.Frozen.Estimate < 0 {
		return domain.ErrConflict
	}
	if r.SpentMicros > r.BudgetLimitMicros || r.HeldMicros > r.BudgetLimitMicros-r.SpentMicros || g.Frozen.Estimate > r.BudgetLimitMicros-r.SpentMicros-r.HeldMicros {
		return ErrBudgetExceeded
	}
	return nil
}
