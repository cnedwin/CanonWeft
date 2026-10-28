package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"novelstudio/internal/domain"
	"slices"
)

type RecipeRetryInput struct {
	ExpectedRevision int64           `json:"expected_revision"`
	OperationID      string          `json:"operation_id"`
	Step             RecipeStepInput `json:"step"`
}

// Retry creates new attempts without changing the old input, output or money
// records. A lost response is replayed through the author's operation key.
func (s *RecipeExecutions) Retry(ctx context.Context, a domain.Actor, project, id string, input RecipeRetryInput) (RecipeExecution, error) {
	if err := recipeTextInput(&input.Step.Input, &input.Step.InputText); err != nil {
		return RecipeExecution{}, err
	}
	if !validName(input.OperationID, 120) {
		return RecipeExecution{}, domain.ErrValidation
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 512*1024 {
		return RecipeExecution{}, domain.ErrValidation
	}
	fingerprint := digest("recipe.retry.v1\x00" + project + "\x00" + id + "\x00" + string(raw))
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return RecipeExecution{}, err
	}
	defer tx.Rollback()
	var prior, hash string
	hash, prior, err = operationReceiptTx(ctx, tx, a, input.OperationID)
	if err == nil {
		if hash != fingerprint || prior != id {
			return RecipeExecution{}, domain.ErrConflict
		}
		tx.Rollback()
		return s.Read(ctx, a, project, id)
	}
	if err != sql.ErrNoRows {
		return RecipeExecution{}, err
	}
	r, err := recipeTx(ctx, tx, project, id)
	if err != nil {
		return r, err
	}
	if r.ActorID != a.ID {
		return r, domain.ErrForbidden
	}
	if r.Revision != input.ExpectedRevision || r.Paused || r.State == "PREPARED" {
		return r, domain.ErrConflict
	}
	if err = s.validateRunTx(ctx, tx, r); err != nil {
		return r, err
	}
	var selected RecipeStepRun
	for _, step := range r.Steps {
		if step.Active && step.Key == input.Step.Key {
			selected = step
			break
		}
	}
	if selected.ID == "" || selected.State == "WAITING" {
		return r, domain.ErrValidation
	}
	if selected.Frozen.Input.NovelChapter != nil {
		if capabilityDigest(input.Step.NovelChapter) != capabilityDigest(selected.Frozen.Input.NovelChapter) || input.Step.NovelDetailID != selected.Frozen.Input.NovelDetailID {
			return r, domain.ErrConflict
		}
		replacement := selected
		replacement.Frozen.Input = input.Step
		if err = s.validateNovelRecipeStepTx(ctx, tx, r, replacement); err != nil {
			return r, err
		}
	}
	if input.Step.MaxOutputTokens < 1 || input.Step.MaxOutputTokens > 131072 {
		return r, domain.ErrValidation
	}
	if err = domain.ValidateRecipeRouting(input.Step.Input, input.Step.Bindings, selected.Frozen.Step.After); err != nil {
		return r, err
	}
	if len(input.Step.Bindings) == 0 {
		if err = recipeContract(selected.Frozen.Agent.Definition.InputSchema, input.Step.Input); err != nil {
			return r, err
		}
	}
	if input.Step.ContextID != "" {
		pack, err := (&Knowledge{IP: s.Generations.IP}).readContextTx(ctx, tx, a.ID, project, r.BranchID, input.Step.ContextID)
		if err != nil {
			return r, err
		}
		if pack.Input.Purpose != contextPurposeForRole(selected.Frozen.Agent.Definition.Role) {
			return r, domain.ErrValidation
		}
	}
	price, err := priceTx(ctx, tx, r.WorkspaceID, input.Step.PriceID)
	if err != nil {
		return r, err
	}
	if price.Currency != r.Currency {
		return r, domain.ErrConflict
	}
	if err = price.Validate(); err != nil {
		return r, err
	}
	config, ref := selected.Frozen.Config, selected.credentialRef
	if input.Step.PriceID == selected.Frozen.Price.ID {
		// Reworking text does not adopt a changed global default. Live credential
		// revocation still applies to the original immutable configuration.
		current, currentRef, readErr := modelTx(ctx, tx, r.WorkspaceID, config.ID)
		if readErr != nil {
			return r, readErr
		}
		if !current.Enabled || !config.Enabled || ref == "" || currentRef != ref {
			return r, domain.ErrConflict
		}
	} else {
		config, ref, err = recipeModelTx(ctx, tx, r.WorkspaceID, selected.Frozen.Agent.Definition.Role, price)
		if err != nil {
			return r, err
		}
	}
	selected.Frozen.Input = input.Step
	selected.Frozen.Price = price
	selected.Frozen.Config = config
	selected.credentialRef = ref
	if err = s.validateStageCapabilityTx(ctx, tx, a, r, selected); err != nil {
		return r, err
	}
	affected := map[string]bool{selected.Key: true}
	// Definitions are topologically ordered. Only transitive dependants lose
	// their confirmations; unrelated completed stages remain reusable.
	for _, step := range r.Frozen.Definition.Definition.Steps {
		for _, before := range step.After {
			if affected[before] {
				affected[step.Key] = true
			}
		}
	}
	if _, err = budgetTx(ctx, tx, r.WorkspaceID); err != nil {
		return r, err
	}
	var uncertain int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipe_steps s JOIN generation_tasks g ON g.id=s.generation_id JOIN model_calls c ON c.id=g.call_id WHERE s.run_id=? AND c.state IN ('UNKNOWN','DISPATCHED')`, r.ID).Scan(&uncertain); err != nil {
		return r, err
	}
	if uncertain != 0 {
		return r, domain.ErrConflict
	}
	if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, project, id); err != nil {
		return r, err
	}
	cancelScope := r
	cancelScope.Steps = nil
	for _, step := range r.Steps {
		if step.Active && affected[step.Key] {
			cancelScope.Steps = append(cancelScope.Steps, step)
		}
	}
	if err = cancelRecipeChildrenTx(ctx, tx, cancelScope); err != nil {
		return r, err
	}
	for _, old := range cancelScope.Steps {
		if old.Attempt == math.MaxInt32 {
			return r, domain.ErrNotReady
		}
		next := RecipeStepRun{ID: NewID(), Key: old.Key, Ordinal: old.Ordinal, Attempt: old.Attempt + 1, Active: true, State: "WAITING", Frozen: old.Frozen, credentialRef: old.credentialRef}
		if old.Key == selected.Key {
			next.Frozen = selected.Frozen
			next.credentialRef = selected.credentialRef
		}
		next.Frozen.Sources = nil
		if _, err = tx.ExecContext(ctx, "UPDATE recipe_steps SET active=FALSE,updated_at=UTC_TIMESTAMP(6) WHERE id=?", old.ID); err != nil {
			return r, err
		}
		if err = insertRecipeStepTx(ctx, tx, r, next); err != nil {
			return r, err
		}
	}
	r.State = "RUNNING"
	r.Reason = ""
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return r, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO operations(actor_id,operation_id,kind,request_hash,result_id) VALUES(?,?,'recipe.retry',?,?)", a.ID, input.OperationID, fingerprint, id); err != nil {
		return r, err
	}
	// A retry may follow a completed workflow; the outbox wakes or restarts the
	// same workflow ID. Parent CAS and attempt IDs prevent duplicate calls.
	// The outbox has one slot per kind/resource. Replace its delivery identity
	// on each committed retry so a late acknowledgement of the prior delivery
	// cannot consume this new wakeup. Old attempt records remain immutable.
	if _, err = tx.ExecContext(ctx, "INSERT INTO outbox(id,project_id,kind,resource_id,payload) VALUES(?,?,'recipe.retry',?,?) ON DUPLICATE KEY UPDATE id=VALUES(id),payload=VALUES(payload),created_at=UTC_TIMESTAMP(6),processed_at=NULL", NewID(), project, id, `{}`); err != nil {
		return r, err
	}
	if err = audit(ctx, tx, a, "recipe.retry", id); err != nil {
		return r, err
	}
	if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, project, id); err != nil {
		return r, err
	}
	if err = tx.Commit(); err != nil {
		return r, err
	}
	return s.Read(ctx, a, project, id)
}

func (s *RecipeExecutions) List(ctx context.Context, a domain.Actor, project, branch string) ([]RecipeExecution, error) {
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = branchTx(ctx, tx, project, branch); err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+recipeColumns+" FROM recipe_runs WHERE project_id=? AND branch_id=? AND (actor_id=? OR ?) ORDER BY created_at DESC,id LIMIT 100", project, branch, a.ID, a.Role == "owner")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RecipeExecution{}
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil && !errors.Is(err, domain.ErrMaterialExpired) {
			return nil, err
		}
		result = append(result, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range result {
		r := &result[i]
		if len(r.expiredMaterial) != 0 {
			marker, err := runtimeRetentionTx(ctx, tx, project, "recipe_runs", r.ID, "", r.expiredMaterial, r.expiredOriginalHash)
			if err != nil {
				return nil, err
			}
			*r = RecipeExecution{ID: r.ID, ProjectID: project, BranchID: branch, State: r.State, Revision: r.Revision, Currency: r.Currency, BudgetLimitMicros: r.BudgetLimitMicros, Retention: marker, Steps: []RecipeStepRun{}}
		}
		if err = recipeFeesTx(ctx, tx, &result[i]); err != nil {
			return nil, err
		}
	}
	return result, nil
}

// Fail only records local workflow failure. It never changes external receipts
// or releases holds, including when the original author has lost access.
func (s *RecipeExecutions) Fail(ctx context.Context, id string) (string, error) {
	meta, err := scanRecipe(s.Generations.IP.Workspace.DB.QueryRowContext(ctx, "SELECT "+recipeColumns+" FROM recipe_runs WHERE id=?", id))
	if err != nil {
		return "", err
	}
	tx, err := s.Generations.IP.Workspace.DB.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if err = lockReceiptProjectTx(ctx, tx, meta.ProjectID); err != nil {
		return "", err
	}
	r, err := recipeTx(ctx, tx, meta.ProjectID, id)
	if err != nil {
		return "", err
	}
	if slices.Contains([]string{"COMPLETED", "CANCELLED", "FAILED", "UNKNOWN"}, r.State) {
		return r.State, nil
	}
	if r.Paused {
		return "PAUSED", nil
	}
	r.State = "BLOCKED"
	r.Reason = "WORKFLOW_INTERRUPTED"
	if err = updateRecipeTx(ctx, tx, &r); err != nil {
		return "", err
	}
	return r.State, tx.Commit()
}
