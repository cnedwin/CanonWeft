package retention

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"novelstudio/internal/domain"
	"novelstudio/internal/models"
	"novelstudio/internal/recipe"
)

func recipeFrozenDocument(row SourceRow, target any) error {
	if financialDigest([]byte(row.Values["frozen"])) != row.Values["frozen_hash"] || authorDocument(row.Values["frozen"], target) != nil {
		return ErrSourceOwnership
	}
	return nil
}

func recipeSchemaAccepts(schema json.RawMessage, data []byte) bool {
	compiled, err := domain.CompileCapabilitySchema(schema)
	if err != nil {
		return false
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	return err == nil && compiled.Validate(value) == nil
}

func (index *SourceIndex) attachRecipeSources(ctx context.Context, project string, materials []ObservedMaterial, read ReadSourceObject) (err error) {
	where := ""
	defer func() {
		if err != nil {
			err = fmt.Errorf("recipe source %s: %w", where, err)
		}
	}()
	for i := range materials {
		m := &materials[i]
		if m.Table != "recipe_runs" && m.Table != "recipe_steps" {
			continue
		}
		if m.Field != "frozen" && m.Field != "input_hash" && m.Field != "output_hash" {
			continue
		}
		row := index.rows[m.Table][m.OwnerKey]
		where = m.Table + "." + m.Field
		runRow := row
		if m.Table == "recipe_steps" {
			var err error
			runRow, err = index.sourceRow("recipe_runs", map[string]string{"id": row.Values["run_id"]})
			if err != nil {
				return err
			}
		}
		var run recipe.RecipeFrozen
		if runRow.Values["project_id"] != project || recipeFrozenDocument(runRow, &run) != nil || run.Protocol != 1 || run.Definition.Definition.Kind != "recipe" {
			return ErrSourceOwnership
		}
		b := contextSourceBuilder{index: index, project: project, branch: runRow.Values["branch_id"], actor: runRow.Values["actor_id"], m: &m.MaterialReference}
		if m.Table == "recipe_runs" {
			version, err := b.capabilityManifest(ctx, domain.CapabilityRef{ID: run.Definition.ID, Hash: run.Definition.Hash, Kind: "recipe"}, run.ManifestHash, read)
			if err != nil || capabilityProjectionHash(version.CapabilityVersion) != capabilityProjectionHash(run.Definition) || !recipeSchemaAccepts(version.Definition.InputSchema, run.Input) {
				return ErrSourceOwnership
			}
			if _, err := b.ip(run.BaseHead, b.branch); err != nil {
				return err
			}
			continue
		}
		var step recipe.RecipeStepFrozen
		ordinal, err := strconv.Atoi(row.Values["ordinal"])
		if err != nil || recipeFrozenDocument(row, &step) != nil || ordinal < 0 || ordinal >= len(run.Definition.Definition.Steps) || capabilityProjectionHash(step.Step) != capabilityProjectionHash(run.Definition.Definition.Steps[ordinal]) || step.Step.Key != row.Values["step_key"] || step.Input.Key != step.Step.Key || step.Step.AgentID != step.Agent.ID || step.Input.InputText != "" {
			return ErrSourceOwnership
		}
		switch m.Field {
		case "frozen":
			if err := b.link(runRow, "frozen", ""); err != nil {
				return err
			}
			version, err := b.capabilityManifest(ctx, domain.CapabilityRef{ID: step.Agent.ID, Hash: step.Agent.Hash, Kind: "agent"}, step.ManifestHash, read)
			if err != nil || capabilityProjectionHash(version.CapabilityVersion) != capabilityProjectionHash(step.Agent) || domain.ValidateRecipeRouting(step.Input.Input, step.Input.Bindings, step.Step.After) != nil {
				return ErrSourceOwnership
			}
			if step.Input.ContextID != "" {
				contextRow, err := index.sourceRow("context_packages", map[string]string{"id": step.Input.ContextID})
				var p domain.ContextPackage
				if err != nil || contextRow.Values["project_id"] != project || contextRow.Values["branch_id"] != b.branch || contextRow.Values["actor_id"] != b.actor || contextJSON([]byte(contextRow.Values["payload"]), &p, contextRow.Values["content_hash"]) != nil || p.IPHead != run.BaseHead {
					return ErrSourceOwnership
				}
				if err := b.link(contextRow, "payload", ""); err != nil {
					return err
				}
			}
			if err := b.recipeQualification(run, step); err != nil {
				return err
			}
		case "input_hash":
			if err := b.recipeInput(ctx, row, step, read); err != nil {
				return err
			}
		case "output_hash":
			if err := b.recipeOutput(ctx, row, step, read); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *contextSourceBuilder) recipeQualification(run recipe.RecipeFrozen, step recipe.RecipeStepFrozen) error {
	if run.Trial {
		if step.Input.BindingID != "" || step.Input.TaskKind != "" {
			return ErrSourceOwnership
		}
		return nil
	}
	binding, err := b.index.sourceRow("capability_bindings", map[string]string{"id": step.Input.BindingID})
	if err != nil || binding.Values["project_id"] != b.project || step.Input.TaskKind == "" || binding.Values["task_kind"] != step.Input.TaskKind {
		return ErrSourceOwnership
	}
	record, err := b.index.sourceRow("capability_qualifications", map[string]string{"id": binding.Values["qualification_id"]})
	if err != nil || record.Values["project_id"] != b.project {
		return ErrSourceOwnership
	}
	_, frozen, err := b.qualificationRun(record.Values["run_id"])
	if err != nil || frozen.Candidate.ID != step.Agent.ID || frozen.Candidate.Hash != step.Agent.Hash || frozen.CandidateManifest != step.ManifestHash || frozen.Input.Role != step.Agent.Definition.Role || frozen.Input.MaxOutputTokens != step.Input.MaxOutputTokens || frozen.Suite.TaskKind != step.Input.TaskKind || capabilityProjectionHash(frozen.Config) != capabilityProjectionHash(step.Config) {
		return ErrSourceOwnership
	}
	for _, field := range []string{"payload", "report_hash"} {
		if err := b.link(record, field, ""); err != nil {
			return err
		}
	}
	return nil
}

func (b *contextSourceBuilder) recipeInput(ctx context.Context, row SourceRow, step recipe.RecipeStepFrozen, read ReadSourceObject) error {
	if err := b.link(row, "frozen", ""); err != nil {
		return err
	}
	data, err := read(ctx, b.project, row.Values["input_hash"])
	if err != nil || !recipeSchemaAccepts(step.Agent.Definition.InputSchema, data) {
		return ErrSourceOwnership
	}
	if len(step.Step.After) > 0 && len(step.Sources) == 0 {
		// Legacy rows did not record attempt identity. Do not bind an old input
		// to whichever predecessor is active or textually similar today.
		b.m.UnboundSources++
		return nil
	}
	if len(step.Sources) != len(step.Step.After) {
		return ErrSourceOwnership
	}
	outputs := map[string]json.RawMessage{}
	for i, pin := range step.Sources {
		if pin.Key != step.Step.After[i] || outputs[pin.Key] != nil || !id32.MatchString(pin.StepID) {
			return ErrSourceOwnership
		}
		source, err := b.index.sourceRow("recipe_steps", map[string]string{"id": pin.StepID})
		ordinal, numberErr := strconv.Atoi(source.Values["ordinal"])
		current, currentErr := strconv.Atoi(row.Values["ordinal"])
		if err != nil || numberErr != nil || currentErr != nil || ordinal >= current || source.Values["project_id"] != b.project || source.Values["run_id"] != row.Values["run_id"] || source.Values["step_key"] != pin.Key || source.Values["output_hash"] != pin.OutputHash {
			return ErrSourceOwnership
		}
		value, err := read(ctx, b.project, pin.OutputHash)
		if err != nil {
			return err
		}
		outputs[pin.Key] = value
		if err := b.link(source, "output_hash", ""); err != nil {
			return err
		}
	}
	want, err := domain.ResolveRecipeInput(step.Input.Input, step.Input.Bindings, step.Step.After, outputs)
	if err != nil || !bytes.Equal(want, data) {
		return ErrSourceOwnership
	}
	return nil
}

func (b *contextSourceBuilder) recipeOutput(ctx context.Context, row SourceRow, step recipe.RecipeStepFrozen, read ReadSourceObject) error {
	child, err := b.index.sourceRow("generation_tasks", map[string]string{"id": row.Values["generation_id"]})
	if err != nil || child.Values["project_id"] != b.project || child.Values["actor_id"] != b.actor || child.Values["branch_id"] != b.branch {
		return ErrSourceOwnership
	}
	var frozen struct {
		RunID     string `json:"recipe_run_id"`
		StepID    string `json:"recipe_step_id"`
		InputHash string `json:"recipe_input_hash"`
	}
	if json.Unmarshal([]byte(child.Values["frozen"]), &frozen) != nil || frozen.RunID != row.Values["run_id"] || frozen.StepID != row.Values["id"] || frozen.InputHash != row.Values["input_hash"] {
		return ErrSourceOwnership
	}
	call, err := b.index.sourceRow("model_calls", map[string]string{"id": child.Values["call_id"]})
	if err != nil || call.Values["project_id"] != b.project {
		return ErrSourceOwnership
	}
	data, err := read(ctx, b.project, call.Values["output_hash"])
	var result models.Result
	if err != nil || contentJSON(data, &result, call.Values["output_hash"]) != nil {
		return ErrSourceOwnership
	}
	output, err := read(ctx, b.project, row.Values["output_hash"])
	if err != nil || string(output) != result.Text || !recipeSchemaAccepts(step.Agent.Definition.OutputSchema, output) {
		return ErrSourceOwnership
	}
	if err := b.link(call, "output_hash", ""); err != nil {
		return err
	}
	if err := b.link(child, "frozen", ""); err != nil {
		return err
	}
	return b.link(row, "input_hash", "")
}

func (b *contextSourceBuilder) recipeChildInput(child SourceRow, runID, stepID, hash, head, contextID string) error {
	stepRow, err := b.index.sourceRow("recipe_steps", map[string]string{"id": stepID})
	if err != nil || stepRow.Values["project_id"] != b.project || stepRow.Values["run_id"] != runID || stepRow.Values["generation_id"] != child.Values["id"] || stepRow.Values["input_hash"] != hash {
		return ErrSourceOwnership
	}
	runRow, err := b.index.sourceRow("recipe_runs", map[string]string{"id": runID})
	var run recipe.RecipeFrozen
	var step recipe.RecipeStepFrozen
	if err != nil || runRow.Values["project_id"] != b.project || runRow.Values["branch_id"] != b.branch || runRow.Values["actor_id"] != b.actor || recipeFrozenDocument(runRow, &run) != nil || recipeFrozenDocument(stepRow, &step) != nil || run.BaseHead != head || step.Input.ContextID != contextID {
		return ErrSourceOwnership
	}
	if err := b.link(stepRow, "frozen", ""); err != nil {
		return err
	}
	return b.link(stepRow, "input_hash", "")
}
