package service

import (
	"context"
	"database/sql"
	"math"
	"novelstudio/internal/domain"
	"slices"
)

func (s *RecipeExecutions) validateRunTx(ctx context.Context, tx *sql.Tx, r RecipeExecution) error {
	if r.CancelRequested || r.RestoredDisabled {
		return domain.ErrConflict
	}
	b, err := branchTx(ctx, tx, r.ProjectID, r.BranchID)
	if err != nil {
		return err
	}
	if b.Head != r.Frozen.BaseHead {
		return domain.ErrConflict
	}
	if err = s.validateNovelReviewTx(ctx, tx, r); err != nil {
		return err
	}
	if err = s.validateCharacterBatchTx(ctx, tx, r); err != nil {
		return err
	}
	for _, step := range r.Steps {
		if step.Active && step.Frozen.Input.NovelChapter != nil {
			if err = s.validateNovelRecipeStepTx(ctx, tx, r, step); err != nil {
				return err
			}
		}
		if step.Active && step.Frozen.Agent.Definition.Key == "shared-memory-consolidation" {
			// Import intentionally advances the target revision. Its atomic receipt
			// allows finishing this already-produced result, never a new dispatch.
			imported, receiptErr := refreshImportedTx(ctx, tx, r, step)
			if receiptErr != nil {
				return receiptErr
			}
			if imported {
				continue
			}
			if len(step.Frozen.Input.Bindings) != 0 {
				return domain.ErrValidation
			}
			if err = (&Memory{IP: s.Generations.IP}).validateExtractionInputTx(ctx, tx, r.ActorID, r.ProjectID, r.BranchID, r.Frozen.BaseHead, step.Frozen.Input.Input); err != nil {
				return err
			}
		}
	}
	caps := &Capabilities{IP: s.Generations.IP}
	manifest, err := caps.manifestTx(ctx, tx, r.ProjectID, r.Frozen.Definition.ID)
	if err != nil {
		return err
	}
	if manifest.Hash != r.Frozen.ManifestHash {
		return domain.ErrConflict
	}
	for _, finding := range manifest.Findings {
		if finding.Code != "QUALIFICATION_REQUIRED" {
			return domain.ErrConflict
		}
	}
	return nil
}

func updateRecipeTx(ctx context.Context, tx *sql.Tx, r *RecipeExecution) error {
	if r.Revision == math.MaxInt64 {
		return domain.ErrNotReady
	}
	if err := captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "recipe_runs", r.ID); err != nil {
		return err
	}
	r.Revision++
	_, err := tx.ExecContext(ctx, "UPDATE recipe_runs SET state=?,revision=?,paused=?,cancel_requested=?,reason=?,updated_at=UTC_TIMESTAMP(6) WHERE id=? AND project_id=?", r.State, r.Revision, r.Paused, r.CancelRequested, r.Reason, r.ID, r.ProjectID)
	if err != nil {
		return err
	}
	return captureDiagnosticSourceTx(ctx, tx, r.ProjectID, "recipe_runs", r.ID)
}

func (s *RecipeExecutions) Start(ctx context.Context, a domain.Actor, project, id string, revision int64) (RecipeExecution, error) {
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return RecipeExecution{}, err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if r.ActorID != a.ID {
		return r, domain.ErrForbidden
	}
	if r.State != "PREPARED" {
		if slices.Contains([]string{"RUNNING", "WAITING_CONFIRMATION", "COMPLETED"}, r.State) && !r.CancelRequested && !r.RestoredDisabled {
			err = recipeFeesTx(ctx, tx, &r)
			return r, err
		}
		return r, domain.ErrConflict
	}
	if r.Revision != revision || r.Paused {
		return r, domain.ErrConflict
	}
	if err = s.validateRunTx(ctx, tx, r); err != nil {
		return r, err
	}
	budget, err := budgetTx(ctx, tx, a.WorkspaceID)
	if err != nil {
		return r, err
	}
	if !budget.Enabled {
		return r, ErrBudgetClosed
	}
	if budget.Currency != r.Currency {
		return r, domain.ErrConflict
	}
	r.State = "RUNNING"
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'recipe.start',?,?)", NewID(), project, id, `{}`); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "recipe.start", id); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *RecipeExecutions) Control(ctx context.Context, a domain.Actor, project, id, action string, revision int64) (RecipeExecution, error) {
	if !slices.Contains([]string{"pause", "resume", "cancel"}, action) {
		return RecipeExecution{}, domain.ErrValidation
	}
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return RecipeExecution{}, err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if r.ActorID != a.ID && a.Role != "owner" {
		return r, domain.ErrForbidden
	}
	if r.RestoredDisabled || r.Revision != revision {
		return r, domain.ErrConflict
	}
	if slices.Contains([]string{"COMPLETED", "CANCELLED"}, r.State) {
		return r, domain.ErrConflict
	}
	if action == "cancel" {
		r.CancelRequested = true
		r.Paused = false
		r.State = "CANCELLED"
		r.Reason = "CANCELLED_BY_AUTHOR"
		if err = cancelRecipeChildrenTx(ctx, tx, r); err != nil {
			return r, err
		}
	} else {
		if err = s.validateRunTx(ctx, tx, r); err != nil {
			return r, err
		}
		// Removing a pause does not retry a terminal or uncertain call. Those
		// states remain intact until an explicit, independently checked rework.
		r.Paused = action == "pause"
		if !r.Paused && r.State == "BLOCKED" {
			r.State = "RUNNING"
			r.Reason = ""
		}
	}
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if err = recipeFeesTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "recipe."+action, id); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func (s *RecipeExecutions) Confirm(ctx context.Context, a domain.Actor, project, id, key string, revision int64, hash string) (RecipeExecution, error) {
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return RecipeExecution{}, err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if a.ID != r.ActorID {
		return r, domain.ErrForbidden
	}
	if r.Revision != revision || r.State != "WAITING_CONFIRMATION" || r.Paused {
		return r, domain.ErrConflict
	}
	if err = s.validateRunTx(ctx, tx, r); err != nil {
		return r, err
	}
	found := false
	for i := range r.Steps {
		step := &r.Steps[i]
		if !step.Active || step.Key != key {
			continue
		}
		if step.State != "WAITING_CONFIRMATION" || hash == "" || step.OutputHash != hash {
			return r, domain.ErrConflict
		}
		step.State = "COMPLETED"
		step.ConfirmedHash = hash
		found = true
		if _, err = tx.ExecContext(ctx, "UPDATE recipe_steps SET state='COMPLETED',confirmed_hash=?,updated_at=UTC_TIMESTAMP(6) WHERE id=?", hash, step.ID); err != nil {
			return r, err
		}
	}
	if !found {
		return r, domain.ErrValidation
	}
	r.State = "RUNNING"
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if err = recipeFeesTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "recipe.confirm", id); err != nil {
		return r, err
	}
	if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, project, id); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func cancelRecipeChildrenTx(ctx context.Context, tx *sql.Tx, r RecipeExecution) error {
	if _, err := budgetTx(ctx, tx, r.WorkspaceID); err != nil {
		return err
	}
	for _, step := range r.Steps {
		if !step.Active || step.GenerationID == "" {
			continue
		}
		g, err := generationTx(ctx, tx, r.ProjectID, step.GenerationID)
		if err != nil {
			return err
		}
		if slices.Contains([]string{"SUCCEEDED", "INVALID_OUTPUT", "REJECTED", "FAILED", "CANCELLED", "RECONCILED"}, g.State) {
			continue
		}
		if g.CallID != "" {
			if err = lockQualificationCallTx(ctx, tx, g.CallID); err != nil {
				return err
			}
			call, err := callTx(ctx, tx, r.WorkspaceID, r.ProjectID, g.CallID)
			if err != nil {
				return err
			}
			if err = captureCallSourcesTx(ctx, tx, r.ProjectID, g.CallID); err != nil {
				return err
			}
			if call.State == "RESERVED" {
				if err = accountQualificationCallTx(ctx, tx, g.CallID, "CANCELLED", 0, true); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE model_budgets SET reserved_micros=reserved_micros-? WHERE workspace_id=?", call.Reserved, r.WorkspaceID); err != nil {
					return err
				}
				if _, err = tx.ExecContext(ctx, "UPDATE model_calls SET state='CANCELLED',cancel_requested=TRUE WHERE id=?", g.CallID); err != nil {
					return err
				}
			} else if _, err = tx.ExecContext(ctx, "UPDATE model_calls SET cancel_requested=TRUE WHERE id=?", g.CallID); err != nil {
				return err
			}
			if err = captureCallSourcesTx(ctx, tx, r.ProjectID, g.CallID); err != nil {
				return err
			}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE generation_tasks SET state='CANCELLED',cancel_requested=TRUE,paused=FALSE WHERE id=?", g.ID); err != nil {
			return err
		}
	}
	return nil
}
