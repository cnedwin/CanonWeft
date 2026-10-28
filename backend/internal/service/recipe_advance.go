package service

import (
	"context"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/objectwriter"
	"novelstudio/internal/recipe"
	"slices"
)

func recipeBlockedReason(err error) string {
	switch {
	case errors.Is(err, ErrBudgetClosed):
		return "BUDGET_CLOSED"
	case errors.Is(err, ErrBudgetExceeded):
		return "BUDGET_EXCEEDED"
	case errors.Is(err, domain.ErrForbidden):
		return "PERMISSION_CHANGED"
	case errors.Is(err, domain.ErrValidation):
		return "INVALID_INPUT"
	case errors.Is(err, domain.ErrConflict):
		return "STALE_INPUT"
	default:
		return "NOT_READY"
	}
}

func (s *RecipeExecutions) block(ctx context.Context, previous RecipeExecution, reason string) (string, error) {
	tx, _, _, err := s.Generations.IP.begin(ctx, domain.Actor{ID: previous.ActorID}, previous.ProjectID, true)
	if err != nil {
		return "BLOCKED", err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, previous.ProjectID, previous.ID)
	if err != nil {
		return "BLOCKED", err
	}
	if r.CancelRequested {
		return "CANCELLED", nil
	}
	if r.Paused {
		return "PAUSED", nil
	}
	if r.Revision != previous.Revision {
		return r.State, nil
	}
	r.State = "BLOCKED"
	r.Reason = reason
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return "BLOCKED", err
	}
	return r.State, tx.Commit()
}

func (s *RecipeExecutions) Advance(ctx context.Context, id string) (string, error) {
	meta, err := scanRecipe(s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+recipeColumns+" FROM recipe_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	tx, _, _, err := s.Generations.IP.begin(ctx, domain.Actor{ID: meta.ActorID}, meta.ProjectID, true)
	if err != nil {
		return "BLOCKED", err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, meta.ProjectID, id)
	if err != nil {
		return "", err
	}
	if r.CancelRequested || r.RestoredDisabled {
		return "CANCELLED", nil
	}
	if r.Paused {
		return "PAUSED", nil
	}
	if slices.Contains([]string{"PREPARED", "COMPLETED", "FAILED", "CANCELLED", "UNKNOWN", "BLOCKED"}, r.State) {
		return r.State, nil
	}
	if err = s.validateRunTx(ctx, tx, r); err != nil {
		r.State = "BLOCKED"
		r.Reason = recipeBlockedReason(err)
		if err = updateRecipeTx(ctx, tx, &r); err != nil {
			return "BLOCKED", err
		}
		return r.State, tx.Commit()
	}
	var step RecipeStepRun
	completed := map[string]RecipeStepRun{}
	for _, item := range r.Steps {
		if !item.Active {
			continue
		}
		if item.State == "COMPLETED" {
			completed[item.Key] = item
			continue
		}
		if step.ID == "" {
			step = item
		}
	}
	if step.ID == "" {
		if len(completed) != len(r.Frozen.Definition.Definition.Steps) {
			return "BLOCKED", domain.ErrNotReady
		}
		last := r.Frozen.Definition.Definition.Steps[len(r.Frozen.Definition.Definition.Steps)-1]
		raw, err := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, completed[last.Key].OutputHash)
		if err != nil {
			return "BLOCKED", err
		}
		if recipeContract(r.Frozen.Definition.Definition.OutputSchema, raw) != nil {
			r.State = "FAILED"
			r.Reason = "RECIPE_OUTPUT_INVALID"
		} else {
			r.State = "COMPLETED"
			r.Reason = ""
		}
		if err = updateRecipeTx(ctx, tx, &r); err != nil {
			return "BLOCKED", err
		}
		return r.State, tx.Commit()
	}
	if step.State == "WAITING_CONFIRMATION" {
		return "WAITING_CONFIRMATION", nil
	}
	for _, key := range step.Frozen.Step.After {
		if _, ok := completed[key]; !ok {
			return "BLOCKED", domain.ErrNotReady
		}
	}
	if step.GenerationID == "" {
		outputs := map[string]json.RawMessage{}
		sources := make([]recipe.Source, 0, len(step.Frozen.Step.After))
		for _, key := range step.Frozen.Step.After {
			raw, err := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, completed[key].OutputHash)
			if err != nil {
				return "BLOCKED", err
			}
			outputs[key] = json.RawMessage(raw)
			sources = append(sources, recipe.Source{Key: key, StepID: completed[key].ID, OutputHash: completed[key].OutputHash})
		}
		resolved, err := domain.ResolveRecipeInput(step.Frozen.Input.Input, step.Frozen.Input.Bindings, step.Frozen.Step.After, outputs)
		if err == nil {
			err = recipeContract(step.Frozen.Agent.Definition.InputSchema, resolved)
		}
		if err != nil {
			r.State = "BLOCKED"
			r.Reason = "INVALID_ROUTED_INPUT"
			// This attempt failed validation before dispatch. Persist that fact so
			// the author can repair this stage without discarding paid predecessors.
			if _, err = tx.ExecContext(ctx, "UPDATE recipe_steps SET state='FAILED',updated_at=UTC_TIMESTAMP(6) WHERE id=?", step.ID); err != nil {
				return "BLOCKED", err
			}
			if err = updateRecipeTx(ctx, tx, &r); err != nil {
				return "BLOCKED", err
			}
			return r.State, tx.Commit()
		}
		object, err := objectwriter.Write(ctx, tx, s.Generations.IP.Blobs, r.ProjectID, resolved)
		if err != nil {
			return "BLOCKED", err
		}
		if step.InputHash != "" && step.InputHash != object.Hash {
			return "BLOCKED", domain.ErrConflict
		}
		step.InputHash = object.Hash
		step.State = "PREPARING"
		if len(step.Frozen.Sources) != 0 && capabilityDigest(step.Frozen.Sources) != capabilityDigest(sources) {
			return "BLOCKED", domain.ErrConflict
		}
		step.Frozen.Sources = sources
		frozen, err := json.Marshal(step.Frozen)
		if err != nil {
			return "BLOCKED", err
		}
		if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, r.ProjectID, r.ID); err != nil {
			return "BLOCKED", err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE recipe_steps SET state='PREPARING',input_hash=?,frozen=?,frozen_hash=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", step.InputHash, frozen, digest(string(frozen)), step.ID); err != nil {
			return "BLOCKED", err
		}
		if err = updateRecipeTx(ctx, tx, &r); err != nil {
			return "BLOCKED", err
		}
		if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, r.ProjectID, r.ID); err != nil {
			return "BLOCKED", err
		}
		if err = tx.Commit(); err != nil {
			return "BLOCKED", err
		}
		input := step.Frozen.Input
		// Novel recipe steps retain the same server-validated chapter identity and
		// planning gates as an individual chapter request.
		request := GenerationInput{recipe: &recipeReference{r.ID, step.ID}, BranchID: r.BranchID, ExpectedIPHead: r.Frozen.BaseHead, Role: step.Frozen.Agent.Definition.Role, ContextID: input.ContextID, PriceID: input.PriceID, OperationID: "recipe-" + step.ID, AgentInput: resolved, Request: models.Request{Prompt: "执行本阶段固定专业契约，只根据指定资料提出候选。不得声称已采用、已发布或已取得资格。", MaxOutputTokens: input.MaxOutputTokens}}
		request.novelChapter = input.NovelChapter
		if input.NovelChapter != nil {
			request.Request.Prompt += "\n" + chapterResearchInstructions
		}
		request.NovelDetailID = input.NovelDetailID
		if r.Frozen.Trial {
			request.CapabilityTrial = true
			request.AgentVersionID = step.Frozen.Agent.ID
		} else {
			request.BindingID = input.BindingID
			request.TaskKind = input.TaskKind
		}
		child, err := s.Generations.Prepare(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, request)
		if err != nil {
			return s.block(ctx, r, recipeBlockedReason(err))
		}
		if _, err = s.Generations.Start(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, child.ID); err != nil {
			// Child insertion advanced the parent revision. Read the actual pin
			// before recording a start failure, while respecting intervening control.
			current, readErr := s.Read(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, r.ID)
			if readErr != nil {
				return "BLOCKED", readErr
			}
			return s.block(ctx, current, recipeBlockedReason(err))
		}
		return "RUNNING", nil
	}
	tx.Rollback()
	child, err := s.Generations.Read(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, step.GenerationID)
	if err != nil {
		return s.block(ctx, r, recipeBlockedReason(err))
	}
	if child.State == "PREPARED" {
		if _, err = s.Generations.Start(ctx, domain.Actor{ID: r.ActorID}, r.ProjectID, child.ID); err != nil {
			return s.block(ctx, r, recipeBlockedReason(err))
		}
		return "RUNNING", nil
	}
	if slices.Contains([]string{"QUEUED", "RUNNING"}, child.State) {
		return "RUNNING", nil
	}
	if child.State != "SUCCEEDED" || child.Output == nil {
		return s.finishStage(ctx, r, step, child, nil, false)
	}
	raw := json.RawMessage(child.Output.Text)
	if err = recipeContract(step.Frozen.Agent.Definition.OutputSchema, raw); err != nil {
		return s.finishStage(ctx, r, step, child, nil, false)
	}
	if narrativeRecipePreset(step.Frozen.Agent.Definition.Key) != "" {
		frozenInput, readErr := s.Generations.IP.Blobs.ReadObject(ctx, r.ProjectID, step.InputHash)
		if readErr != nil || digest(string(frozenInput)) != step.InputHash {
			return "BLOCKED", domain.ErrNotReady
		}
		if err = validateNarrativeRecipeOutput(step.Frozen.Agent.Definition.Key, frozenInput, raw); err != nil {
			return s.finishStage(ctx, r, step, child, nil, false)
		}
	}
	if err = s.validateNovelReviewOutput(ctx, r, step, raw); err != nil {
		if !errors.Is(err, domain.ErrValidation) {
			return s.block(ctx, r, recipeBlockedReason(err))
		}
		return s.finishStage(ctx, r, step, child, nil, false)
	}
	if err = s.validateCharacterBatchOutput(ctx, r, step, raw); err != nil {
		if !errors.Is(err, domain.ErrValidation) {
			return s.block(ctx, r, recipeBlockedReason(err))
		}
		return s.finishStage(ctx, r, step, child, nil, false)
	}
	return s.finishStage(ctx, r, step, child, raw, true)
}

func (s *RecipeExecutions) finishStage(ctx context.Context, previous RecipeExecution, step RecipeStepRun, child Generation, output []byte, valid bool) (string, error) {
	tx, _, _, err := s.Generations.IP.begin(ctx, domain.Actor{ID: previous.ActorID}, previous.ProjectID, true)
	if err != nil {
		return "BLOCKED", err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, previous.ProjectID, previous.ID)
	if err != nil {
		return "BLOCKED", err
	}
	if r.CancelRequested || r.RestoredDisabled {
		return "CANCELLED", nil
	}
	if r.Paused {
		return "PAUSED", nil
	}
	if r.Revision != previous.Revision {
		return r.State, nil
	}
	if err = s.validateRunTx(ctx, tx, r); err != nil {
		return "BLOCKED", err
	}
	current, err := recipeStepByID(r, step.ID)
	if err != nil {
		return "BLOCKED", err
	}
	if !current.Active || current.GenerationID != child.ID || current.State != "RUNNING" {
		return r.State, nil
	}
	g, err := generationTx(ctx, tx, r.ProjectID, child.ID)
	if err != nil {
		return "BLOCKED", err
	}
	if g.State != child.State || g.Cancelled {
		return "BLOCKED", domain.ErrConflict
	}
	hash := ""
	if valid {
		object, err := objectwriter.Write(ctx, tx, s.Generations.IP.Blobs, r.ProjectID, output)
		if err != nil {
			return "BLOCKED", err
		}
		hash = object.Hash
	}
	state := "FAILED"
	if child.State == "UNKNOWN" || child.State == "RECONCILED" {
		state = "UNKNOWN"
	}
	if valid {
		state = "COMPLETED"
		if current.Frozen.Step.WaitForConfirmation {
			state = "WAITING_CONFIRMATION"
		}
	}
	if _, err = tx.ExecContext(ctx, "UPDATE recipe_steps SET state=?,output_hash=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", state, hash, step.ID); err != nil {
		return "BLOCKED", err
	}
	r.State = state
	if state == "COMPLETED" {
		r.State = "RUNNING"
	}
	if state == "FAILED" {
		r.Reason = "CHILD_OUTPUT_INVALID"
	}
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return "BLOCKED", err
	}
	if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, r.ProjectID, r.ID); err != nil {
		return "BLOCKED", err
	}
	if err = tx.Commit(); err != nil {
		return "BLOCKED", err
	}
	// Advance may enqueue the next stage; dispatch still belongs to the
	// generation worker and remains subject to the parent budget and controls.
	if state == "COMPLETED" {
		return s.Advance(ctx, r.ID)
	}
	return r.State, nil
}
