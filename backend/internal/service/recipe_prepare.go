package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"novelstudio/internal/domain"
)

func recipeContract(schema, input json.RawMessage) error {
	if _, err := domain.ResolveRecipeInput(input, nil, nil, nil); err != nil {
		return err
	}
	contract, err := domain.CompileCapabilitySchema(schema)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(input))
	if err != nil || contract.Validate(value) != nil {
		return domain.ErrValidation
	}
	return nil
}

func recipeTextInput(raw *json.RawMessage, text *string) error {
	if *text == "" {
		return nil
	}
	if len(*raw) != 0 && string(*raw) != "null" || len(*text) > 256*1024 {
		return domain.ErrValidation
	}
	*raw = json.RawMessage(*text)
	*text = ""
	_, err := domain.ResolveRecipeInput(*raw, nil, nil, nil)
	return err
}

func recipeModelTx(ctx context.Context, tx *sql.Tx, workspace, role string, price PriceCard) (domain.ModelConfig, string, error) {
	var config domain.ModelConfig
	var connection string
	var revision int64
	if err := tx.QueryRowContext(ctx, "SELECT connection_id,connection_revision FROM model_bindings WHERE workspace_id=? AND role=?", workspace, role).Scan(&connection, &revision); err != nil {
		return config, "", domain.ErrNotReady
	}
	if price.ConnectionID != connection || price.ConnectionRevision != revision {
		return config, "", domain.ErrConflict
	}
	current, ref, err := modelTx(ctx, tx, workspace, connection)
	if err != nil {
		return config, "", err
	}
	var raw []byte
	var pinned string
	if err = tx.QueryRowContext(ctx, "SELECT config,credential_ref FROM model_connection_versions WHERE connection_id=? AND revision=?", connection, revision).Scan(&raw, &pinned); err != nil {
		return config, "", err
	}
	if json.Unmarshal(raw, &config) != nil || !config.Enabled || !current.Enabled || ref == "" || ref != pinned {
		return config, "", domain.ErrConflict
	}
	return config, pinned, nil
}

func (s *RecipeExecutions) Prepare(ctx context.Context, a domain.Actor, project string, input RecipeExecutionInput) (RecipeExecution, error) {
	var r RecipeExecution
	if err := recipeTextInput(&input.Input, &input.InputText); err != nil {
		return r, err
	}
	for i := range input.Steps {
		if err := recipeTextInput(&input.Steps[i].Input, &input.Steps[i].InputText); err != nil {
			return r, err
		}
	}
	if !validName(input.OperationID, 120) || len(input.Steps) < 1 || len(input.Steps) > 32 || input.BudgetLimitMicros < 1 || input.BudgetLimitMicros > 1_000_000_000_000 || (input.Currency != "USD" && input.Currency != "CNY") {
		return r, domain.ErrValidation
	}
	raw, err := json.Marshal(input)
	if err != nil || len(raw) > 2*1024*1024 {
		return r, domain.ErrValidation
	}
	fingerprint := digest("recipe.execution.v1\x00" + project + "\x00" + string(raw))
	tx, a, _, err := s.Generations.IP.begin(ctx, a, project, true)
	if err != nil {
		return r, err
	}
	defer tx.Rollback()
	var prior, priorHash string
	prior, priorHash, err = requestReceiptTx(ctx, tx, a, "recipe_runs", input.OperationID, false)
	if err == nil {
		if priorHash != fingerprint {
			return r, domain.ErrConflict
		}
		tx.Rollback()
		return s.Read(ctx, a, project, prior)
	}
	if err != sql.ErrNoRows {
		return r, err
	}
	b, err := branchTx(ctx, tx, project, input.BranchID)
	if err != nil {
		return r, err
	}
	if b.Head != input.ExpectedIPHead {
		return r, domain.ErrConflict
	}
	caps := &Capabilities{IP: s.Generations.IP}
	version, err := capabilityTx(ctx, tx, project, input.RecipeVersionID)
	if err != nil {
		return r, err
	}
	if version.Definition.Kind != "recipe" || len(version.Definition.Steps) != len(input.Steps) {
		return r, domain.ErrValidation
	}
	if err = recipeContract(version.Definition.InputSchema, input.Input); err != nil {
		return r, err
	}
	manifest, err := caps.manifestTx(ctx, tx, project, version.ID)
	if err != nil {
		return r, err
	}
	for _, finding := range manifest.Findings {
		if finding.Code != "QUALIFICATION_REQUIRED" {
			return r, domain.ErrNotReady
		}
	}
	r = RecipeExecution{ID: NewID(), ProjectID: project, BranchID: input.BranchID, ActorID: a.ID, WorkspaceID: a.WorkspaceID, State: "PREPARED", Revision: 1, Currency: input.Currency, BudgetLimitMicros: input.BudgetLimitMicros, Frozen: RecipeFrozen{Protocol: 1, Definition: version, ManifestHash: manifest.Hash, BaseHead: b.Head, Input: input.Input, Trial: input.Trial}, Steps: []RecipeStepRun{}}
	for i, definition := range version.Definition.Steps {
		stage := input.Steps[i]
		if stage.Key != definition.Key || stage.MaxOutputTokens < 1 || stage.MaxOutputTokens > 131072 {
			return r, domain.ErrValidation
		}
		agent, err := capabilityTx(ctx, tx, project, definition.AgentID)
		if err != nil {
			return r, err
		}
		m, err := caps.manifestTx(ctx, tx, project, agent.ID)
		if err != nil {
			return r, err
		}
		if agent.Definition.Kind != "agent" || !m.TrialReady {
			return r, domain.ErrNotReady
		}
		if _, err = domain.ResolveRecipeInput(stage.Input, nil, nil, nil); err != nil {
			return r, err
		}
		// Validate targets and predecessor declarations before any model call.
		// Actual source fields can only be checked after that predecessor returns.
		if err = domain.ValidateRecipeRouting(stage.Input, stage.Bindings, definition.After); err != nil {
			return r, err
		}
		if len(stage.Bindings) == 0 {
			if err = recipeContract(agent.Definition.InputSchema, stage.Input); err != nil {
				return r, err
			}
		}
		if agent.Definition.Key == "shared-memory-consolidation" {
			if len(stage.Bindings) != 0 {
				return r, domain.ErrValidation
			}
			if err = (&Memory{IP: s.Generations.IP}).validateExtractionInputTx(ctx, tx, a.ID, project, b.ID, b.Head, stage.Input); err != nil {
				return r, err
			}
		}
		if stage.ContextID != "" {
			context, err := (&Knowledge{IP: s.Generations.IP}).readContextTx(ctx, tx, a.ID, project, b.ID, stage.ContextID)
			if err != nil {
				return r, err
			}
			if context.Input.Purpose != contextPurposeForRole(agent.Definition.Role) {
				return r, domain.ErrValidation
			}
			if context.Memory != nil {
				if err = refreshMatchesStep(context.Memory.Request.Refresh, RecipeStepRun{Frozen: RecipeStepFrozen{Agent: agent, Input: stage}}); err != nil {
					return r, err
				}
			}
		}
		price, err := priceTx(ctx, tx, a.WorkspaceID, stage.PriceID)
		if err != nil {
			return r, err
		}
		if price.Currency != r.Currency {
			return r, domain.ErrValidation
		}
		if err = price.Validate(); err != nil {
			return r, err
		}
		config, ref, err := recipeModelTx(ctx, tx, a.WorkspaceID, agent.Definition.Role, price)
		if err != nil {
			return r, err
		}
		step := RecipeStepRun{ID: NewID(), Key: stage.Key, Ordinal: i, Attempt: 1, Active: true, State: "WAITING", credentialRef: ref, Frozen: RecipeStepFrozen{Step: definition, Input: stage, ManifestHash: m.Hash, Agent: agent, Config: config, Price: price}}
		if err = s.validateStageCapabilityTx(ctx, tx, a, r, step); err != nil {
			return r, err
		}
		r.Steps = append(r.Steps, step)
	}
	for _, step := range r.Steps {
		if step.Frozen.Input.NovelChapter != nil {
			if err = s.validateNovelRecipeStepTx(ctx, tx, r, step); err != nil {
				return r, err
			}
		} else if step.Frozen.Input.NovelDetailID != "" {
			return r, domain.ErrValidation
		}
	}
	if err = s.validateNovelReviewTx(ctx, tx, r); err != nil {
		return r, err
	}
	if err = s.validateCharacterBatchTx(ctx, tx, r); err != nil {
		return r, err
	}
	frozen, _ := json.Marshal(r.Frozen)
	_, err = tx.ExecContext(ctx, `INSERT INTO recipe_runs(id,workspace_id,project_id,branch_id,actor_id,operation_id,request_hash,frozen,frozen_hash,state,currency,budget_limit_micros) VALUES(?,?,?,?,?,?,?,?,?,'PREPARED',?,?)`, r.ID, a.WorkspaceID, project, b.ID, a.ID, input.OperationID, fingerprint, frozen, digest(string(frozen)), r.Currency, r.BudgetLimitMicros)
	if err != nil {
		return r, err
	}
	for _, step := range r.Steps {
		if err = insertRecipeStepTx(ctx, tx, r, step); err != nil {
			return r, err
		}
	}
	if err = audit(ctx, tx, a, "recipe.prepare", r.ID); err != nil {
		return r, err
	}
	if err = s.Generations.IP.captureRecipeSourceTx(ctx, tx, project, r.ID); err != nil {
		return r, err
	}
	return r, tx.Commit()
}

func insertRecipeStepTx(ctx context.Context, tx *sql.Tx, r RecipeExecution, step RecipeStepRun) error {
	raw, _ := json.Marshal(step.Frozen)
	_, err := tx.ExecContext(ctx, `INSERT INTO recipe_steps(id,run_id,project_id,step_key,ordinal,attempt,frozen,frozen_hash,credential_ref) VALUES(?,?,?,?,?,?,?,?,?)`, step.ID, r.ID, r.ProjectID, step.Key, step.Ordinal, step.Attempt, raw, digest(string(raw)), step.credentialRef)
	return err
}

func (s *RecipeExecutions) validateStageCapabilityTx(ctx context.Context, tx *sql.Tx, a domain.Actor, r RecipeExecution, step RecipeStepRun) error {
	input := step.Frozen.Input
	if r.Frozen.Trial {
		if input.BindingID != "" || input.TaskKind != "" {
			return domain.ErrValidation
		}
		return nil
	}
	if input.BindingID == "" || input.TaskKind == "" {
		return domain.ErrNotReady
	}
	binding, err := bindingTx(ctx, tx, r.ProjectID, input.BindingID)
	if err != nil {
		return err
	}
	if !binding.Enabled || binding.TaskKind != input.TaskKind {
		return domain.ErrConflict
	}
	if err = requireCurrentBindingTx(ctx, tx, r.ProjectID, binding.ID, binding.TaskKind); err != nil {
		return err
	}
	q := &Qualifications{IP: s.Generations.IP}
	record, err := q.recordTx(ctx, tx, r.ProjectID, binding.QualificationID)
	if err != nil {
		return err
	}
	if record.Frozen.Candidate.ID != step.Frozen.Agent.ID || record.Frozen.CandidateManifest != step.Frozen.ManifestHash || record.Frozen.Input.Role != step.Frozen.Agent.Definition.Role || record.Frozen.Input.MaxOutputTokens != input.MaxOutputTokens || capabilityDigest(record.Frozen.Config) != capabilityDigest(step.Frozen.Config) {
		return domain.ErrConflict
	}
	return q.validateRecordTx(ctx, tx, a, r.ProjectID, record)
}
