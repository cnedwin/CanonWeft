package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"novelstudio/internal/domain"
	"novelstudio/internal/recipe"
	"novelstudio/internal/retention"
)

type RecipeExecutions struct{ Generations *Generations }

type RecipeStepInput = recipe.RecipeStepInput

type RecipeExecutionInput struct {
	RecipeVersionID   string            `json:"recipe_version_id"`
	BranchID          string            `json:"branch_id"`
	ExpectedIPHead    string            `json:"expected_ip_head"`
	OperationID       string            `json:"operation_id"`
	Trial             bool              `json:"trial"`
	InputText         string            `json:"input_text,omitempty"`
	Input             json.RawMessage   `json:"input"`
	Currency          string            `json:"currency"`
	BudgetLimitMicros int64             `json:"budget_limit_micros"`
	Steps             []RecipeStepInput `json:"steps"`
}

type RecipeFrozen = recipe.RecipeFrozen
type RecipeStepFrozen = recipe.RecipeStepFrozen

type RecipeExecution struct {
	Retention           *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial     []byte
	expiredOriginalHash string
	ID                  string          `json:"id"`
	ProjectID           string          `json:"-"`
	BranchID            string          `json:"branch_id"`
	ActorID             string          `json:"-"`
	WorkspaceID         string          `json:"-"`
	State               string          `json:"state"`
	Revision            int64           `json:"revision"`
	Paused              bool            `json:"paused"`
	CancelRequested     bool            `json:"cancel_requested"`
	RestoredDisabled    bool            `json:"restored_disabled"`
	Reason              string          `json:"reason"`
	Currency            string          `json:"currency"`
	BudgetLimitMicros   int64           `json:"budget_limit_micros"`
	SpentMicros         int64           `json:"spent_micros"`
	HeldMicros          int64           `json:"held_micros"`
	UncertainCalls      int             `json:"uncertain_calls"`
	Frozen              RecipeFrozen    `json:"frozen"`
	Steps               []RecipeStepRun `json:"steps"`
}

type RecipeStepRun struct {
	Retention           *RuntimeRetention `json:"retention,omitempty"`
	expiredMaterial     []byte
	expiredOriginalHash string
	ID                  string           `json:"id"`
	Key                 string           `json:"key"`
	Ordinal             int              `json:"ordinal"`
	Attempt             int              `json:"attempt"`
	Active              bool             `json:"active"`
	GenerationID        string           `json:"generation_id"`
	State               string           `json:"state"`
	InputHash           string           `json:"input_hash"`
	OutputHash          string           `json:"output_hash"`
	ConfirmedHash       string           `json:"confirmed_hash"`
	Frozen              RecipeStepFrozen `json:"frozen"`
	Output              json.RawMessage  `json:"output,omitempty"`
	InputText           string           `json:"input_text,omitempty"`
	OutputText          string           `json:"output_text,omitempty"`
	credentialRef       string
}

const recipeColumns = "id,workspace_id,project_id,branch_id,actor_id,state,revision,paused,cancel_requested,restored_disabled,reason,currency,budget_limit_micros,frozen,frozen_hash"

func scanRecipe(row interface{ Scan(...any) error }) (RecipeExecution, error) {
	var r RecipeExecution
	var raw []byte
	var hash string
	err := row.Scan(&r.ID, &r.WorkspaceID, &r.ProjectID, &r.BranchID, &r.ActorID, &r.State, &r.Revision, &r.Paused, &r.CancelRequested, &r.RestoredDisabled, &r.Reason, &r.Currency, &r.BudgetLimitMicros, &raw, &hash)
	if err == sql.ErrNoRows {
		return r, domain.ErrForbidden
	}
	if err != nil {
		return r, err
	}
	if err := rejectExpiredRuntime(raw, "recipe_runs.frozen"); err != nil {
		r.expiredMaterial = raw
		r.expiredOriginalHash = hash
		return r, err
	}
	if digest(string(raw)) != hash || json.Unmarshal(raw, &r.Frozen) != nil || r.Frozen.Protocol != 1 || r.Revision < 1 || r.BudgetLimitMicros < 1 {
		return r, domain.ErrNotReady
	}
	return r, nil
}

func recipeTx(ctx context.Context, tx *sql.Tx, project, id string, history ...bool) (RecipeExecution, error) {
	readHistory := len(history) > 0 && history[0]
	r, err := scanRecipe(tx.QueryRowContext(ctx, "SELECT "+recipeColumns+" FROM recipe_runs WHERE id=? AND project_id=? FOR UPDATE", id, project))
	if errors.Is(err, domain.ErrMaterialExpired) && readHistory {
		r.Retention, err = runtimeRetentionTx(ctx, tx, project, "recipe_runs", id, "", r.expiredMaterial, r.expiredOriginalHash)
	}
	if err != nil {
		return r, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,step_key,ordinal,attempt,active,COALESCE(generation_id,''),state,input_hash,output_hash,confirmed_hash,frozen,frozen_hash,credential_ref FROM recipe_steps WHERE run_id=? AND project_id=? ORDER BY ordinal,attempt FOR UPDATE`, id, project)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	r.Steps = []RecipeStepRun{}
	for rows.Next() {
		var step RecipeStepRun
		var raw []byte
		var hash string
		if err = rows.Scan(&step.ID, &step.Key, &step.Ordinal, &step.Attempt, &step.Active, &step.GenerationID, &step.State, &step.InputHash, &step.OutputHash, &step.ConfirmedHash, &raw, &hash, &step.credentialRef); err != nil {
			return r, err
		}
		if err := rejectExpiredRuntime(raw, "recipe_steps.frozen"); err != nil {
			if errors.Is(err, domain.ErrMaterialExpired) && readHistory {
				step.expiredMaterial, step.expiredOriginalHash = raw, hash
				r.Steps = append(r.Steps, step)
				continue
			}
			return r, err
		}
		if digest(string(raw)) != hash || json.Unmarshal(raw, &step.Frozen) != nil || step.Key != step.Frozen.Step.Key || step.Attempt < 1 {
			return r, domain.ErrNotReady
		}
		r.Steps = append(r.Steps, step)
	}
	if err := rows.Err(); err != nil {
		return r, err
	}
	if err := rows.Close(); err != nil {
		return r, err
	}
	for i := range r.Steps {
		step := &r.Steps[i]
		for _, material := range []struct{ field, hash string }{{"input_hash", step.InputHash}, {"output_hash", step.OutputHash}} {
			expired, readErr := runtimeObjectRetentionTx(ctx, tx, project, "recipe_steps", step.ID, material.field, material.hash)
			if readErr != nil {
				return r, readErr
			}
			if expired != nil {
				if !readHistory {
					return r, domain.ErrMaterialExpired
				}
				step.Retention = expired
				step.Frozen = RecipeStepFrozen{}
			}
		}
		if step.expiredMaterial != nil {
			step.Retention, err = runtimeRetentionTx(ctx, tx, project, "recipe_steps", step.ID, "", step.expiredMaterial, step.expiredOriginalHash)
			if err != nil {
				return r, err
			}
			if _, marked := retention.RetiredLabelHash(step.Key); marked {
				step.Key = "expired-" + step.ID
			}
		}
	}
	return r, nil
}

func recipeFeesTx(ctx context.Context, tx *sql.Tx, r *RecipeExecution) error {
	// The existing call ledger is the only monetary fact. All attempts, including
	// inactive ones and UNKNOWN reservations, remain included under this root.
	var wrongCurrency int
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(c.charged_micros),0),COALESCE(SUM(CASE WHEN c.state IN ('RESERVED','DISPATCHED','UNKNOWN') THEN c.reserved_micros ELSE 0 END),0),COALESCE(SUM(a.call_id IS NULL OR a.currency<>?),0),COALESCE(SUM(c.state IN ('DISPATCHED','UNKNOWN')),0)
 FROM recipe_steps s JOIN generation_tasks g ON g.id=s.generation_id JOIN model_calls c ON c.id=g.call_id LEFT JOIN model_call_accounting a ON a.call_id=c.id WHERE s.run_id=? AND s.project_id=?`, r.Currency, r.ID, r.ProjectID).Scan(&r.SpentMicros, &r.HeldMicros, &wrongCurrency, &r.UncertainCalls)
	if err != nil {
		return err
	}
	if wrongCurrency != 0 || r.SpentMicros < 0 || r.HeldMicros < 0 {
		return domain.ErrNotReady
	}
	return nil
}

func (s *RecipeExecutions) Read(ctx context.Context, a domain.Actor, project, id string) (RecipeExecution, error) {
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, false)
	if err != nil {
		return RecipeExecution{}, err
	}
	defer tx.Rollback()
	r, err := recipeTx(ctx, tx, project, id, true)
	if err != nil {
		return r, err
	}
	if r.ActorID != a.ID && a.Role != "owner" {
		return RecipeExecution{}, domain.ErrForbidden
	}
	if err = recipeFeesTx(ctx, tx, &r); err != nil {
		return r, err
	}
	for i := range r.Steps {
		step := &r.Steps[i]
		if r.Retention != nil || step.Retention != nil {
			continue
		}
		step.InputText = string(step.Frozen.Input.Input)
		if step.OutputHash == "" {
			continue
		}
		raw, readErr := s.Generations.IP.Blobs.ReadObject(ctx, project, step.OutputHash)
		if readErr != nil || digest(string(raw)) != step.OutputHash {
			return r, domain.ErrNotReady
		}
		step.Output = json.RawMessage(raw)
		step.OutputText = string(raw)
	}
	return r, nil
}

func recipeStepByID(r RecipeExecution, id string) (RecipeStepRun, error) {
	for _, step := range r.Steps {
		if step.ID == id {
			return step, nil
		}
	}
	return RecipeStepRun{}, domain.ErrForbidden
}
